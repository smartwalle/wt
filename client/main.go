package main

import (
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v2"
	"github.com/smartwalle/net4go"
	"time"
	"wt/protocol"
)

func main() {
	var config = webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:192.168.1.12:3478"},
			},
		},
	}

	var peer, err = webrtc.NewPeerConnection(config)
	if err != nil {
		fmt.Println(err)
		return
	}

	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
	})

	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
	})

	offer, err := peer.CreateOffer(nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	peer.SetLocalDescription(offer)

	var joinReq = &protocol.JoinRoomReq{}
	joinReq.RoomId = "111"
	joinReq.UserId = "1"
	joinReq.SessionDescription = &offer

	c, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:6656/ws", nil)
	if err != nil {
		fmt.Println(err)
		return
	}

	var h = &ConnHandler{peer: peer}
	var p = &protocol.WSProtocol{}

	var nConn = net4go.NewWsConn(c, p, h)

	nConn.AsyncWritePacket(&protocol.Packet{Type: protocol.PTJoinRoomReq, JoinRoomReq: joinReq}, 0)

	select {}
}

// ConnHandler
type ConnHandler struct {
	peer *webrtc.PeerConnection
}

func (this *ConnHandler) OnMessage(conn net4go.Conn, packet net4go.Packet) bool {
	var nPacket = packet.(*protocol.Packet)

	switch nPacket.Type {
	case protocol.PTJoinRoomRsp:
		this.peer.SetRemoteDescription(*nPacket.JoinRoomRsp.SessionDescription)

		var dc, _ = this.peer.CreateDataChannel("haha", nil)
		for {
			dc.SendText("hehehehe")
			time.Sleep(time.Second * 2)
		}
	}

	return true
}

func (this *ConnHandler) OnClose(net4go.Conn, error) {

}
