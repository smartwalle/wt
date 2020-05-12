package protocol

import (
	"encoding/json"
	"fmt"
	"github.com/pion/webrtc/v2"
	"github.com/smartwalle/net4go"
	"io"
)

// WSProtocol
type WSProtocol struct {
}

func (this *WSProtocol) Marshal(p net4go.Packet) ([]byte, error) {
	return json.Marshal(p)
}

func (this *WSProtocol) Unmarshal(r io.Reader) (net4go.Packet, error) {
	var p *Packet
	if err := json.NewDecoder(r).Decode(&p); err != nil {

		fmt.Println(err)
		return nil, err
	}
	return p, nil
}

type PacketType int

const (
	PTJoinRoomReq PacketType = 1
	PTJoinRoomRsp PacketType = 2
)

// Packet
type Packet struct {
	Type        PacketType   `json:"type"`
	JoinRoomReq *JoinRoomReq `json:"join_room_req"`
	JoinRoomRsp *JoinRoomRsp `json:"join_room_rsp"`
}

type JoinRoomReq struct {
	RoomId             string                     `json:"room_id"`
	UserId             string                     `json:"user_id"`
	SessionDescription *webrtc.SessionDescription `json:"session_description"`
}

type JoinRoomRsp struct {
	RoomId             string                     `json:"room_id"`
	UserId             string                     `json:"user_id"`
	SessionDescription *webrtc.SessionDescription `json:"session_description"`
}

func (this *Packet) Marshal() ([]byte, error) {
	return nil, nil
}

func (this *Packet) Unmarshal(data []byte) error {
	return nil
}
