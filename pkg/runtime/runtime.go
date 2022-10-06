package runtime

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/yuyz0112/sunmao-ui-go-binding/pkg/sunmao"
)

type Runtime struct {
	e                        *echo.Echo
	conns                    map[int]*websocket.Conn
	appBuilder               *sunmao.AppBuilder
	moduleBuilders           []*sunmao.ModuleBuilder
	reloadWhenWsDisconnected bool
	handlers                 map[string]func(m *Message, connId int) error
	hooks                    map[string]func(connId int) error
	uiDir                    string
}

func New(uiDir string) *Runtime {
	e := echo.New()

	r := &Runtime{
		e:                        e,
		conns:                    map[int]*websocket.Conn{},
		reloadWhenWsDisconnected: true,
		handlers:                 map[string]func(m *Message, connId int) error{},
		uiDir:                    uiDir,
	}

	return r
}

var (
	upgrader = websocket.Upgrader{}
)

type Message struct {
	Type    string         `json:"type"`
	Handler string         `json:"handler"`
	Params  any            `json:"params"`
	Store   map[string]any `json:"store"`
}

func (r *Runtime) Run() {
	if r.appBuilder == nil {
		log.Fatalln("please load app before run")
	}

	r.e.Use(middleware.Gzip())

	r.e.Static("/assets", fmt.Sprintf("%v/dist/assets", r.uiDir))

	r.e.GET("/", func(c echo.Context) error {
		buf, err := os.ReadFile(fmt.Sprintf("%v/dist/index.html", r.uiDir))
		if err != nil {
			return err
		}

		handlers := []string{}
		for k := range r.handlers {
			handlers = append(handlers, k)
		}

		modules := make([]any, len(r.moduleBuilders))
		for i, b := range r.moduleBuilders {
			modules[i] = b.ValueOf()
		}

		optionsBuf, err := json.Marshal(map[string]interface{}{
			"application":              r.appBuilder.ValueOf(),
			"modules":                  modules,
			"reloadWhenWsDisconnected": r.reloadWhenWsDisconnected,
			"handlers":                 handlers,
		})
		if err != nil {
			return err
		}

		html := strings.Replace(string(buf),
			"/* APPLICATION */",
			fmt.Sprintf("options = Object.assign(options, %v)", string(optionsBuf)), 1)
		return c.HTML(http.StatusOK, html)
	})

	connId := 0

	r.e.GET("/ws", func(c echo.Context) error {
		ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
		if err != nil {
			return err
		}
		connId++
		r.conns[connId] = ws
		defer func() {
			delete(r.conns, connId)
			ws.Close()
		}()

		connectedHook, ok := r.hooks["connected"]
		if ok {
			connectedHook(connId)
		}

		for {
			_, msgBytes, err := ws.ReadMessage()
			if err != nil {
				if strings.Contains(err.Error(), "close 1001") {
					disconnectedHook, ok := r.hooks["disconnected"]
					if ok {
						disconnectedHook(connId)
					}

					break
				} else {
					c.Logger().Error(err)
				}
			}

			msg := &Message{}

			err = json.Unmarshal(msgBytes, msg)
			if err != nil {
				// ignore
			}

			if msg.Type == "Action" {
				handler, ok := r.handlers[msg.Handler]
				if ok {
					handler(msg, connId)
				}
			}
		}

		return nil
	})

	r.e.Logger.Fatal(r.e.Start(":8999"))
}

func (r *Runtime) LoadApp(builder *sunmao.AppBuilder) error {
	r.appBuilder = builder
	return nil
}

func (r *Runtime) LoadModule(builder ...*sunmao.ModuleBuilder) error {
	r.moduleBuilders = builder
	return nil
}

func (r *Runtime) Handle(handler string, fn func(m *Message, connId int) error) {
	r.handlers[handler] = fn
}

func (r *Runtime) On(hook string, fn func(connId int) error) {
	r.hooks[hook] = fn
}

type ExecuteTarget struct {
	Id         string
	Method     string
	Parameters any
}

// maybe this is a bad idea, but currently we let connId == nil to represent broadcasting
func (r *Runtime) Execute(target *ExecuteTarget, connId *int) error {
	for id, ws := range r.conns {
		if connId != nil && id != *connId {
			continue
		}

		msg, err := json.Marshal(map[string]interface{}{
			"type":        "UiMethod",
			"componentId": target.Id,
			"name":        target.Method,
			"parameters":  target.Parameters,
		})
		if err != nil {
			return err
		}

		err = ws.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			return err
		}
	}
	return nil
}

type ServerState struct {
	r         *Runtime
	initState any
	Id        string
}

func (r *Runtime) NewServerState(id string, initState any) *ServerState {
	return &ServerState{
		r:         r,
		initState: initState,
		Id:        id,
	}
}

func (s *ServerState) AsComponent() sunmao.BaseComponentBuilder {
	t := s.r.appBuilder.NewComponent().Type("core/v1/dummy").Id(s.Id).
		Trait(
			s.r.appBuilder.NewTrait().Type("core/v1/state").
				Properties(map[string]interface{}{
					"key":          "state",
					"initialValue": s.initState,
				}))
	return t
}

func (s *ServerState) SetState(newState any, connId *int) error {
	return s.r.Execute(&ExecuteTarget{
		Id:     s.Id,
		Method: "setValue",
		Parameters: map[string]interface{}{
			"key":   "state",
			"value": newState,
		},
	}, connId)
}
