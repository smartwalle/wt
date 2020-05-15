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
	PTPubReq PacketType = 1
	PTPubRsp PacketType = 2

	PTSubReq PacketType = 3
	PTSubRsp PacketType = 4

	PTNewPubNotify PacketType = 5
	PTDelPubNotify PacketType = 6
)

// Packet
type Packet struct {
	Type         PacketType    `json:"type"`
	PubReq       *PubReq       `json:"pub_req,omitempty"`
	PubRsp       *PubRsp       `json:"pub_rsp,omitempty"`
	SubReq       *SubReq       `json:"sub_req,omitempty"`
	SubRsp       *SubRsp       `json:"sub_rsp,omitempty"`
	NewPubNotify *NewPubNotify `json:"new_pub_notify,omitempty"`
	DelPubNotify *DelPubNotify `json:"del_pub_notify,omitempty"`
}

type PubReq struct {
	RoomId             string                     `json:"room_id"`
	UserId             string                     `json:"user_id"`
	SessionDescription *webrtc.SessionDescription `json:"session_description"`
}

type PubRsp struct {
	RoomId             string                     `json:"room_id"`
	UserId             string                     `json:"user_id"`
	SessionDescription *webrtc.SessionDescription `json:"session_description"`
}

type SubReq struct {
	RoomId             string                     `json:"room_id"`
	UserId             string                     `json:"user_id"`
	ToUserId           string                     `json:"to_user_id"`
	SessionDescription *webrtc.SessionDescription `json:"session_description"`
}

type SubRsp struct {
	RoomId             string                     `json:"room_id"`
	UserId             string                     `json:"user_id"`
	ToUserId           string                     `json:"to_user_id"`
	SessionDescription *webrtc.SessionDescription `json:"session_description"`
}

type NewPubNotify struct {
	RoomId string   `json:"room_id"`
	UserId []string `json:"user_id"`
}

type DelPubNotify struct {
	RoomId string `json:"room_id"`
	UserId string `json:"user_id"`
}

func (this *Packet) Marshal() ([]byte, error) {
	return nil, nil
}

func (this *Packet) Unmarshal(data []byte) error {
	return nil
}
