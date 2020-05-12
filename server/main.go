package main

import (
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v2"
	"github.com/pion/webrtc/v2/pkg/media"
	"github.com/smartwalle/net4go"
	"html/template"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"wt/protocol"
)

func main() {
	var rm = NewRoomManager()
	var h = &ConnHandler{rm: rm}
	var p = &protocol.WSProtocol{}

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
		t, _ := template.ParseFiles("sfu.html")
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
	case protocol.PTJoinRoomReq:
		var joinReq = nPacket.JoinRoomReq

		var room = this.rm.GetRoom(joinReq.RoomId)
		var remoteSession = room.Join(joinReq.UserId, joinReq.SessionDescription)

		var joinRsp = &protocol.JoinRoomRsp{}
		joinRsp.UserId = joinReq.UserId
		joinRsp.RoomId = joinReq.RoomId
		joinRsp.SessionDescription = remoteSession
		conn.AsyncWritePacket(&protocol.Packet{Type: protocol.PTJoinRoomRsp, JoinRoomRsp: joinRsp}, 0)
	}

	return true
}

func (this *ConnHandler) OnClose(conn net4go.Conn, err error) {
	fmt.Println("close", err)
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
	id    string
	mu    sync.Mutex
	users map[string]*User
}

func NewRoom(id string) *Room {
	var r = &Room{}
	r.id = id
	r.users = make(map[string]*User)
	return r
}

func (this *Room) Join(userId string, remoteSession *webrtc.SessionDescription) (localSession *webrtc.SessionDescription) {
	this.mu.Lock()
	var user = this.users[userId]
	if user == nil {
		user = NewUse(userId)
		this.users[userId] = user
	} else {
		if user.peer != nil {
			user.peer.Close()
		}
	}
	this.mu.Unlock()

	var m = webrtc.MediaEngine{}
	m.RegisterCodec(webrtc.NewRTPOpusCodec(webrtc.DefaultPayloadTypeOpus, 48000))
	m.RegisterCodec(webrtc.NewRTPVP8Codec(webrtc.DefaultPayloadTypeVP8, 90000))

	var api = webrtc.NewAPI(webrtc.WithMediaEngine(m))

	var config = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlanWithFallback,
	}

	var peer, err = api.NewPeerConnection(config)
	if err != nil {
		return
	}
	user.peer = peer

	peer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio)
	//peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo)

	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		fmt.Println("OnICEConnectionStateChange", state)
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Println("OnConnectionStateChange", state)

		if state == webrtc.PeerConnectionStateDisconnected {
			user.peer.Close()
			user.peer = nil

			this.mu.Lock()
			delete(this.users, user.id)
			this.mu.Unlock()
		}
	})
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		channel.OnOpen(func() {
			fmt.Println("open data channel")
		})
		channel.OnMessage(func(msg webrtc.DataChannelMessage) {
			fmt.Println(string(msg.Data))
		})
	})
	if user.audioTrack, err = peer.NewTrack(webrtc.DefaultPayloadTypeOpus, rand.Uint32(), "audio", "pion"); err != nil {
		fmt.Println(err)
		return
	}
	user.peer.AddTrack(user.audioTrack)

	if user.videoTrack, err = peer.NewTrack(webrtc.DefaultPayloadTypeVP8, rand.Uint32(), "video", "pion"); err != nil {
		fmt.Println(err)
		return
	}
	user.peer.AddTrack(user.videoTrack)

	peer.OnTrack(func(track *webrtc.Track, receiver *webrtc.RTPReceiver) {
		if track.PayloadType() == webrtc.DefaultPayloadTypeVP8 || track.PayloadType() == webrtc.DefaultPayloadTypeVP9 || track.PayloadType() == webrtc.DefaultPayloadTypeH264 {
			rtpBuf := make([]byte, 1400)
			for {
				i, err := track.Read(rtpBuf)
				if err != nil {
					return
				}
				for _, user := range this.users {
					if user.id != userId {
						w, err := user.WriteVideo(rtpBuf[:i])
						fmt.Println("video", w, err)
						if err != nil && err != io.ErrClosedPipe {
							fmt.Println(err)
							return
						}
					}
				}
			}
		} else {
			rtpBuf := make([]byte, 1400)
			for {
				i, err := track.Read(rtpBuf)
				if err != nil {
					return
				}
				for _, user := range this.users {
					if user.id != userId {
						w, err := user.WriteAudio(rtpBuf[:i])
						fmt.Println("audio", w, err)
						if err != nil && err != io.ErrClosedPipe {
							fmt.Println(err)
							return
						}
					}
				}
			}
		}
	})

	peer.SetRemoteDescription(*remoteSession)

	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	peer.SetLocalDescription(answer)

	localSession = &answer

	return localSession
}

// User
type User struct {
	mu         sync.Mutex
	id         string
	peer       *webrtc.PeerConnection
	videoTrack *webrtc.Track
	audioTrack *webrtc.Track
}

func NewUse(userId string) *User {
	var u = &User{}
	u.id = userId
	return u
}

func (this *User) WriteVideo(b []byte) (n int, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.videoTrack == nil {
		return 0, nil
	}
	return this.videoTrack.Write(b)
}

func (this *User) WriteAudio(b []byte) (n int, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.audioTrack == nil {
		return 0, nil
	}
	return this.audioTrack.Write(b)
}


func saveToDisk(i media.Writer, track *webrtc.Track) {
	defer func() {
		if err := i.Close(); err != nil {
			panic(err)
		}
	}()

	for {
		packet, err := track.ReadRTP()
		if err != nil {
			panic(err)
		}

		if err := i.WriteRTP(packet); err != nil {
			panic(err)
		}
	}
}