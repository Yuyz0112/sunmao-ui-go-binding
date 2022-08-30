package runtime

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	echo "github.com/labstack/echo/v4"
	"github.com/yuyz0112/sunmao-ui-go-binding/pkg/sunmao"
	"log"
	"net/http"
	"os"
	"strings"
)

type Runtime struct {
	e                        *echo.Echo
	conns                    map[int]*websocket.Conn
	appBuilder               *sunmao.AppBuilder
	reloadWhenWsDisconnected bool
	handlers                 map[string]func(m *Message) error
	uiDir                    string
	store                    map[string]any
	storeCh                  chan map[string]any
}

func New(uiDir string) *Runtime {
	e := echo.New()

	var store map[string]any
	storeCh := make(chan map[string]any)

	r := &Runtime{
		e:                        e,
		conns:                    map[int]*websocket.Conn{},
		reloadWhenWsDisconnected: true,
		handlers:                 map[string]func(m *Message) error{},
		uiDir:                    uiDir,
		storeCh:                  storeCh,
		store:                    store,
	}

	go func() {
		for {
			select {
			case s := <-storeCh:
				r.store = s
			}
		}
	}()

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

		optionsBuf, err := json.Marshal(map[string]interface{}{
			"application":              r.appBuilder.ValueOf(),
			"reloadWhenWsDisconnected": r.reloadWhenWsDisconnected,
			"wsUrl":                    "ws://localhost:8999/ws",
			"handlers":                 handlers,
		})
		if err != nil {
			return err
		}

		html := strings.Replace(string(buf),
			"/* APPLICATION */",
			fmt.Sprintf("options = %v", string((optionsBuf))), 1)
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

		for {
			_, msgBytes, err := ws.ReadMessage()
			if err != nil {
				c.Logger().Error(err)
			}

			msg := &Message{}

			err = json.Unmarshal(msgBytes, msg)
			if err != nil {
				// ignore
			}

			if msg.Type == "Action" {
				handler, ok := r.handlers[msg.Handler]
				if ok {
					handler(msg)
				}
			}

			if msg.Type == "StoreChange" && msg.Store != nil {
				select {
				case r.storeCh <- msg.Store:
				default:

				}
			}
		}
	})

	defer func() {
		close(r.storeCh)
	}()

	r.e.Logger.Fatal(r.e.Start(":8999"))
}

func (r *Runtime) LoadApp(builder *sunmao.AppBuilder) error {
	r.appBuilder = builder
	return nil
}

func (r *Runtime) Handle(handler string, fn func(m *Message) error) {
	r.handlers[handler] = fn
}

type ExecuteTarget struct {
	Id         string
	Method     string
	Parameters any
}

func (r *Runtime) Execute(target *ExecuteTarget) error {
	for _, ws := range r.conns {
		msg, err := json.Marshal(map[string]interface{}{
			"type":        "UiMethod",
			"componentId": target.Id,
			"name":        target.Method,
			"parameters":  target.Parameters,
		})
		if err != nil {
			return err
		}
		ws.WriteMessage(websocket.TextMessage, msg)
	}
	return nil
}

func (r *Runtime) GetStore() map[string]any {
	return r.store
}

func (r *Runtime) GetStoreCh() chan map[string]any {
	return r.storeCh
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

func (s *ServerState) SetState(newState any) error {
	return s.r.Execute(&ExecuteTarget{
		Id:     s.Id,
		Method: "setValue",
		Parameters: map[string]interface{}{
			"key":   "state",
			"value": newState,
		},
	})
}
