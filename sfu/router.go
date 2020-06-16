package sfu

import (
	"errors"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v2"
	"github.com/smartwalle/log4go"
	"io"
	"math/rand"
	"sync"
	"time"
)

var ErrUnpublished = errors.New("unpublished")

type Router struct {
	id     string
	mu     *sync.Mutex
	closed bool

	wtAPI    *webrtc.API
	wtConfig *webrtc.Configuration

	publisher  *webrtc.PeerConnection
	subscribes map[string]*webrtc.PeerConnection

	videoTrack *webrtc.Track
	audioTrack *webrtc.Track

	trackInfos map[uint32]*trackInfo
}

func NewRouter(id string, api *webrtc.API, config *webrtc.Configuration) (*Router, error) {
	var r = &Router{}
	r.id = id
	r.mu = &sync.Mutex{}
	r.wtAPI = api
	r.wtConfig = config
	r.subscribes = make(map[string]*webrtc.PeerConnection)
	r.trackInfos = make(map[uint32]*trackInfo)
	return r, nil
}

func (this *Router) GetId() string {
	return this.id
}

func (this *Router) initPub(remoteSession *webrtc.SessionDescription) (localSession *webrtc.SessionDescription, err error) {
	var peer *webrtc.PeerConnection

	if peer, err = this.wtAPI.NewPeerConnection(*this.wtConfig); err != nil {
		return nil, err
	}

	if _, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if _, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}

	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed {
			if peer == nil {
				return
			}
			peer.Close()
			peer = nil
			log4go.Printf("%s 取消发布 \n", this.id)
		}
	})

	if this.audioTrack == nil {
		if this.audioTrack, err = peer.NewTrack(webrtc.DefaultPayloadTypeOpus, rand.Uint32(), webrtc.RTPCodecTypeAudio.String(), this.id); err != nil {
			log4go.Println(err)
			return
		}
	}
	//peer.AddTrack(this.audioTrack)

	if this.videoTrack == nil {
		if this.videoTrack, err = peer.NewTrack(webrtc.DefaultPayloadTypeVP8, rand.Uint32(), webrtc.RTPCodecTypeVideo.String(), this.id); err != nil {
			log4go.Println(err)
			return
		}
	}
	//peer.AddTrack(this.videoTrack)

	peer.OnTrack(func(track *webrtc.Track, receiver *webrtc.RTPReceiver) {
		switch track.Kind() {
		case webrtc.RTPCodecTypeVideo:
			go func() {
				peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: track.SSRC()}})

				ticker := time.NewTicker(time.Second * 3)
				for range ticker.C {
					if peer != nil {
						if err := peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: track.SSRC()}}); err != nil {
							return
						}
					}
				}
			}()
			this.rewriteRTP(track, this.videoTrack)
		case webrtc.RTPCodecTypeAudio:
			this.rewriteRTP(track, this.audioTrack)
		}
	})

	if err = peer.SetRemoteDescription(*remoteSession); err != nil {
		return nil, err
	}

	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}
	if err = peer.SetLocalDescription(answer); err != nil {
		return nil, err
	}

	this.publisher = peer

	return this.publisher.LocalDescription(), nil
}

func (this *Router) Publish(remoteSession *webrtc.SessionDescription) (localSession *webrtc.SessionDescription, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()

	if this.publisher != nil {
		this.publisher.Close()
	}

	return this.initPub(remoteSession)
}

func (this *Router) addSub(subscriber string, remoteSession *webrtc.SessionDescription) (localSession *webrtc.SessionDescription, err error) {
	if this.videoTrack == nil || this.audioTrack == nil {
		return nil, ErrUnpublished
	}

	var peer *webrtc.PeerConnection

	if peer, err = this.wtAPI.NewPeerConnection(*this.wtConfig); err != nil {
		return nil, err
	}
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed {
			this.mu.Lock()
			defer this.mu.Unlock()
			var sub, ok = this.subscribes[subscriber]
			if ok && sub == peer {
				delete(this.subscribes, subscriber)
				peer.Close()
				peer = nil
				log4go.Printf("%s 取消订阅 %s \n", subscriber, this.id)
			}
		} else if state == webrtc.PeerConnectionStateConnected {
			this.mu.Lock()
			defer this.mu.Unlock()
			this.subscribes[subscriber] = peer

			log4go.Printf("%s 订阅 %s 成功 \n", subscriber, this.id)
		}
	})

	peer.AddTrack(this.videoTrack)
	peer.AddTrack(this.audioTrack)

	if err = peer.SetRemoteDescription(*remoteSession); err != nil {
		return nil, err
	}

	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}
	if err = peer.SetLocalDescription(answer); err != nil {
		return
	}

	return peer.LocalDescription(), nil
}

func (this *Router) Subscribe(subscriber string, remoteSession *webrtc.SessionDescription) (localSession *webrtc.SessionDescription, err error) {
	var sub = this.subscribes[subscriber]
	if sub != nil {
		sub.Close()
	}

	localSession, err = this.addSub(subscriber, remoteSession)
	if err != nil {
		return nil, err
	}

	log4go.Printf("%s 订阅 %s\n", subscriber, this.id)

	return localSession, nil
}

func (this *Router) Unsubscribe(subscriber string) {
	this.mu.Lock()
	var sub = this.subscribes[subscriber]
	delete(this.subscribes, subscriber)
	defer this.mu.Unlock()

	if sub != nil {
		sub.Close()
	}
}

type trackInfo struct {
	timestamp      uint32
	sequenceNumber uint16
}

func (this *Router) rewriteRTP(src, dst *webrtc.Track) error {
	var err error
	defer func() {
		log4go.Println(this.id, "rewrite end...", err)
	}()
	log4go.Println(this.id, "rewrite begin...")

	var info = this.trackInfos[dst.SSRC()]
	if info == nil {
		info = &trackInfo{}
		this.trackInfos[dst.SSRC()] = info
	}

	var lastTimestamp uint32 // 用于记录当前 track 最后一包的 timestamp 信息
	var tempTimestamp uint32 // 中间变量
	var packet *rtp.Packet

	for ; ; info.sequenceNumber++ {
		packet, err = src.ReadRTP()
		if err != nil {
			return err
		}

		// 记录当前包的 timestamp  信息
		tempTimestamp = packet.Timestamp
		if lastTimestamp == 0 {
			// 如果 lastTimestamp 为 0，是第一个数据包，则把该包的 timestamp 设置为 0
			packet.Timestamp = 0
		} else {
			// 如果 lastTimestamp 不为 0，不是第一个数据包，则把该包的 timestamp 设置为距离上一包的时间差
			packet.Timestamp -= lastTimestamp
		}
		// 将 lastTimestamp 设置为当前包的 timestamp
		lastTimestamp = tempTimestamp

		// 修正并记录下该 track 的正常 timestamp 信息
		info.timestamp += packet.Timestamp

		packet.Timestamp = info.timestamp
		packet.SequenceNumber = info.sequenceNumber
		packet.SSRC = dst.SSRC()
		err = dst.WriteRTP(packet)
		if err != nil && err != io.ErrClosedPipe {
			return err
		}
	}
	return nil
}

func (this *Router) LocalDescription() *webrtc.SessionDescription {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.publisher != nil {
		return this.publisher.LocalDescription()
	}
	return nil
}

func (this *Router) Close() error {
	this.mu.Lock()
	defer this.mu.Unlock()

	log4go.Println(this.id, "close")

	if this.closed {
		return nil
	}

	this.closed = true
	if this.publisher != nil {
		this.publisher.Close()
	}
	for key, sub := range this.subscribes {
		sub.Close()
		delete(this.subscribes, key)
	}
	return nil
}
