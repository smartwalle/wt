package main

import (
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v2"
	"github.com/smartwalle/log4go"
	"github.com/smartwalle/net4go"
	"html/template"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"
	"wt/protocol"
)

var api *webrtc.API

var config = webrtc.Configuration{
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
	log4go.Println("close", err)
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

	var peer, err = api.NewPeerConnection(config)
	if err != nil {
		log4go.Println(err)
		return
	}
	user.peer = peer

	if user.audioTrack, err = peer.NewTrack(webrtc.DefaultPayloadTypeOpus, rand.Uint32(), "audio", "audio"); err != nil {
		log4go.Println(err)
		return
	}
	user.peer.AddTrack(user.audioTrack)

	if user.videoTrack, err = peer.NewTrack(webrtc.DefaultPayloadTypeVP8, rand.Uint32(), "video", "video"); err != nil {
		log4go.Println(err)
		return
	}
	user.peer.AddTrack(user.videoTrack)

	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log4go.Println("OnICEConnectionStateChange", state)
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log4go.Println("OnConnectionStateChange", state)

		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed {
			if user != nil && user.peer != nil {
				user.peer.Close()
				user.peer = nil
			}

			this.mu.Lock()
			delete(this.users, user.id)
			this.mu.Unlock()
		}
	})

	peer.OnTrack(func(track *webrtc.Track, receiver *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			go func() {
				peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: track.SSRC()}})
				ticker := time.NewTicker(time.Second * 3)
				defer ticker.Stop()
				for range ticker.C {
					if err := peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: track.SSRC()}}); err != nil {
						log4go.Println(err)
						return
					}
				}
			}()

			for {
				rtp, err := track.ReadRTP()
				if err != nil {
					log4go.Println(err)
					return
				}

				for _, user := range this.users {
					if user.id != userId {
						//if user.videoTrack == nil {
						//	continue
						//}

						if err = user.WriteVideoRTP(rtp); err != nil {
							log4go.Println(err)
							return
						}

						//rtp.SSRC = user.videoTrack.SSRC()
						//if err := user.videoTrack.WriteRTP(rtp); err != nil {
						//	log4go.Println(err)
						//	return
						//}
					}

				}
			}

			//rtpBuf := make([]byte, 1400)
			//for {
			//	i, err := track.Read(rtpBuf)
			//	if err != nil {
			//		return
			//	}
			//	for _, user := range this.users {
			//		if user.id != userId {
			//			w, err := user.WriteVideo(rtpBuf[:i])
			//			fmt.Println("video", w, err)
			//			if err != nil && err != io.ErrClosedPipe {
			//				fmt.Println(err)
			//				return
			//			}
			//		}
			//	}
			//}
		} else {
			rtpBuf := make([]byte, 1460)
			for {
				i, err := track.Read(rtpBuf)
				if err != nil {
					return
				}
				for _, user := range this.users {
					if user.id != userId {
						if user.audioTrack == nil {
							continue
						}
						_, err = user.WriteAudio(rtpBuf[:i])
						if err != nil && err != io.ErrClosedPipe {
							fmt.Println(err)
							return
						}
					}
				}
			}

			//for {
			//	rtp, err := track.ReadRTP()
			//	if err != nil {
			//		log4go.Println(err)
			//		return
			//	}
			//	for _, user := range this.users {
			//		if user.id != userId {
			//			if err = user.WriteAudioRTP(rtp); err != nil {
			//				log4go.Println(err)
			//				return
			//			}
			//			//if user.audioTrack == nil {
			//			//	continue
			//			//}
			//			//
			//			//rtp.SSRC = user.audioTrack.SSRC()
			//			//if err = user.audioTrack.WriteRTP(rtp); err != nil {
			//			//	log4go.Println(err)
			//			//	return
			//			//}
			//		}
			//	}
			//}
		}
	})

	if err = peer.SetRemoteDescription(*remoteSession); err != nil {
		log4go.Println(err)
		return
	}

	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		log4go.Println(err)
		return
	}
	if err = peer.SetLocalDescription(answer); err != nil {
		log4go.Println(err)
		return
	}

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

func (this *User) WriteVideoRTP(p *rtp.Packet) error {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.videoTrack == nil {
		return nil
	}
	p.SSRC = this.videoTrack.SSRC()
	return this.videoTrack.WriteRTP(p)
}

func (this *User) WriteAudioRTP(p *rtp.Packet) (err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.audioTrack == nil {
		return nil
	}
	p.SSRC = this.audioTrack.SSRC()
	return this.audioTrack.WriteRTP(p)
}

func (this *User) WriteAudio(b []byte) (int, error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.audioTrack == nil {
		return 0, nil
	}
	return this.audioTrack.Write(b)
}
