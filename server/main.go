package main

import (
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v2"
	"github.com/smartwalle/log4go"
	"github.com/smartwalle/net4go"
	"html/template"
	"net/http"
	"sync"
	"wt/protocol"
	"wt/sfu"
)

var api *webrtc.API

var config = &webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:222.73.105.251:3478"},
		},
	},
}

func main() {
	var rm = NewRoomManager()
	var h = &ConnHandler{rm: rm}
	var p = &protocol.WSProtocol{}

	var m = webrtc.MediaEngine{}
	m.RegisterCodec(webrtc.NewRTPOpusCodec(webrtc.DefaultPayloadTypeOpus, 48000))
	m.RegisterCodec(webrtc.NewRTPVP8Codec(webrtc.DefaultPayloadTypeVP8, 90000))

	api = webrtc.NewAPI(webrtc.WithMediaEngine(m))

	serveHTTP(h, p)
}

func serveHTTP(h net4go.Handler, p net4go.Protocol) {
	var upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	upgrader.CheckOrigin = func(r *http.Request) bool {
		return true
	}

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		var c, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		net4go.NewWsConn(c, p, h, net4go.WithReadLimitSize(0))
	})
	http.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		t, _ := template.ParseFiles("index.html")
		t.Execute(writer, nil)
	})

	fmt.Println("http://localhost:6656/")
	http.ListenAndServe(":6656", nil)
}

// ConnHandler
type ConnHandler struct {
	rm *RoomManager
}

func (this *ConnHandler) OnMessage(conn net4go.Conn, packet net4go.Packet) bool {
	var nPacket = packet.(*protocol.Packet)
	switch nPacket.Type {
	case protocol.PTPubReq:
		var pubReq = nPacket.PubReq

		conn.Set("room_id", pubReq.RoomId)
		conn.Set("user_id", pubReq.UserId)

		var room = this.rm.GetRoom(pubReq.RoomId)
		var localSession = room.Pub(pubReq.UserId, conn, pubReq.SessionDescription)

		var pubRsp = &protocol.PubRsp{}
		pubRsp.UserId = pubReq.UserId
		pubRsp.RoomId = pubReq.RoomId
		pubRsp.SessionDescription = localSession
		conn.AsyncWritePacket(&protocol.Packet{Type: protocol.PTPubRsp, PubRsp: pubRsp}, 0)

		var ids = make([]string, 0, 0)
		for id, _ := range room.conns {
			ids = append(ids, id)
		}

		var pubNotify = &protocol.NewPubNotify{}
		pubNotify.UserId = ids
		pubNotify.RoomId = pubReq.RoomId

		for _, c := range room.conns {
			c.AsyncWritePacket(&protocol.Packet{Type: protocol.PTNewPubNotify, NewPubNotify: pubNotify}, 0)
		}

	case protocol.PTSubReq:
		var subReq = nPacket.SubReq

		log4go.Println(subReq.UserId, "->", subReq.ToUserId)

		var room = this.rm.GetRoom(subReq.RoomId)
		var localSession = room.Sub(subReq.UserId, subReq.ToUserId, conn, subReq.SessionDescription)

		var subRsp = &protocol.SubRsp{}
		subRsp.RoomId = subReq.RoomId
		subRsp.UserId = subReq.UserId
		subRsp.ToUserId = subReq.ToUserId
		subRsp.SessionDescription = localSession
		conn.AsyncWritePacket(&protocol.Packet{Type: protocol.PTSubRsp, SubRsp: subRsp}, 0)
	}

	return true
}

func (this *ConnHandler) OnClose(conn net4go.Conn, err error) {
	log4go.Println("close", err)

	var rValue = conn.Get("room_id")
	if rValue != nil {
		var roomId = rValue.(string)
		var room = this.rm.GetRoom(roomId)

		if room != nil {
			var uValue = conn.Get("user_id")
			if uValue != nil {

				room.mu.Lock()

				var userId = uValue.(string)
				delete(room.conns, userId)

				var router = room.routers[userId]
				if router != nil {
					router.Close()
				}
				delete(room.routers, userId)

				room.mu.Unlock()

				var delNotify = &protocol.DelPubNotify{}
				delNotify.RoomId = roomId
				delNotify.UserId = userId
				for _, c := range room.conns {
					c.AsyncWritePacket(&protocol.Packet{Type: protocol.PTDelPubNotify, DelPubNotify: delNotify}, 0)
				}
			}
		}
	}
}

// RoomManager
type RoomManager struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

func NewRoomManager() *RoomManager {
	var rm = &RoomManager{}
	rm.rooms = make(map[string]*Room)
	return rm
}

func (this *RoomManager) GetRoom(roomId string) *Room {
	this.mu.Lock()
	defer this.mu.Unlock()
	var room = this.rooms[roomId]

	if room == nil {
		room = NewRoom(roomId)
		this.rooms[roomId] = room
	}
	return room
}

// Room
type Room struct {
	id string
	mu sync.Mutex

	conns   map[string]net4go.Conn
	routers map[string]*sfu.Router
}

func NewRoom(id string) *Room {
	var r = &Room{}
	r.id = id
	r.conns = make(map[string]net4go.Conn)
	r.routers = make(map[string]*sfu.Router)
	return r
}

func (this *Room) Sub(userId, toUserId string, conn net4go.Conn, remoteSession *webrtc.SessionDescription) *webrtc.SessionDescription {
	this.mu.Lock()
	defer this.mu.Unlock()

	this.conns[userId] = conn

	var router = this.routers[toUserId]
	if router == nil {
		log4go.Println("router not exist:", userId, "->", toUserId)
		return nil
	}

	local, err := router.Subscribe(userId, remoteSession)
	if err != nil {
		log4go.Println(err)
	}
	return local
}

func (this *Room) Pub(userId string, conn net4go.Conn, remoteSession *webrtc.SessionDescription) *webrtc.SessionDescription {
	this.mu.Lock()
	defer this.mu.Unlock()

	// 清除旧连接绑定的信息
	var c = this.conns[userId]
	if c != nil {
		c.Del("room_id")
		c.Del("user_id")
	}

	var router = this.routers[userId]
	if router == nil {
		var err error
		router, err = sfu.NewRouter(userId, api, config)
		if err != nil {
			return nil
		}

		this.conns[userId] = conn
		this.routers[userId] = router

		log4go.Println(userId, "创建成功")
	}

	localSession, err := router.Publish(remoteSession)
	if err != nil {
		return nil
	}

	return localSession

	//var router = this.routers[userId]
	//if router != nil {
	//	localSession, err := router.Publish(remoteSession)
	//	if err != nil {
	//		return nil
	//	}
	//
	//	var c = this.conns[userId]
	//	c.Del("room_id")
	//	c.Del("user_id")
	//
	//	this.conns[userId] = conn
	//	this.routers[userId] = router
	//	log4go.Println(userId, "重新发布")
	//
	//	if c != nil {
	//		c.Close()
	//	}
	//
	//	return localSession
	//}
	//
	//router, err := sfu.NewRouter(userId, api, config, remoteSession)
	//if err != nil {
	//	return nil
	//}
	//
	//log4go.Println(userId, "创建成功")
	//
	//this.conns[userId] = conn
	//this.routers[userId] = router
	//
	//return router.LocalDescription()

	//this.mu.Lock()
	//defer this.mu.Unlock()
	//
	//var router = this.routers[userId]
	//
	//fmt.Println(userId, jType, router)
	//
	//if jType == "pub" {
	//	if router != nil {
	//		return
	//	}
	//
	//	nRouter, err := sfu.NewRouter(userId, api, config, remoteSession)
	//	if err != nil {
	//		return nil
	//	}
	//	router = nRouter
	//
	//	this.routers[userId] = router
	//
	//	return router.LocalDescription()
	//} else {
	//	if router == nil {
	//		return
	//	}
	//	localSession, err := router.Subscribe(fmt.Sprintf("%d", rand.Uint32()), remoteSession)
	//	if err != nil {
	//		return nil
	//	}
	//
	//	return localSession
	//}
	//
	//return
}
