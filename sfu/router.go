package sfu

import (
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v2"
	"github.com/smartwalle/log4go"
	"io"
	"math/rand"
	"sync"
	"time"
)

type Router struct {
	id       string
	mu       *sync.Mutex
	wtAPI    *webrtc.API
	wtConfig *webrtc.Configuration

	peer *webrtc.PeerConnection
	subs map[string]*webrtc.PeerConnection

	videoTrack *webrtc.Track
	audioTrack *webrtc.Track
}

func NewRouter(id string, api *webrtc.API, config *webrtc.Configuration, remoteSession *webrtc.SessionDescription) (*Router, error) {
	var r = &Router{}
	r.id = id
	r.mu = &sync.Mutex{}
	r.wtAPI = api
	r.wtConfig = config
	r.subs = make(map[string]*webrtc.PeerConnection)

	if err := r.addPub(remoteSession); err != nil {
		return nil, err
	}
	return r, nil
}

func (this *Router) GetId() string {
	return this.id
}

func (this *Router) addPub(remoteSession *webrtc.SessionDescription) (err error) {
	if this.peer, err = this.wtAPI.NewPeerConnection(*this.wtConfig); err != nil {
		return err
	}

	if _, err = this.peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}
	if _, err = this.peer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		return err
	}

	this.peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log4go.Println(state)
	})
	this.peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed {
			if this.peer != nil {
				this.peer.Close()
			}
		}
	})
	this.peer.OnDataChannel(func(channel *webrtc.DataChannel) {
	})

	if this.audioTrack, err = this.peer.NewTrack(webrtc.DefaultPayloadTypeOpus, rand.Uint32(), webrtc.RTPCodecTypeAudio.String(), this.id); err != nil {
		log4go.Println(err)
		return
	}
	this.peer.AddTrack(this.audioTrack)

	if this.videoTrack, err = this.peer.NewTrack(webrtc.DefaultPayloadTypeVP8, rand.Uint32(), webrtc.RTPCodecTypeVideo.String(), this.id); err != nil {
		log4go.Println(err)
		return
	}
	this.peer.AddTrack(this.videoTrack)

	this.peer.OnTrack(func(track *webrtc.Track, receiver *webrtc.RTPReceiver) {
		//var nTrack, err = this.peer.NewTrack(track.PayloadType(), rand.Uint32(), track.Kind().String(), track.ID())
		//if err != nil {
		//	return
		//}

		switch track.Kind() {
		case webrtc.RTPCodecTypeVideo:
			go func() {
				this.peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: track.SSRC()}})

				ticker := time.NewTicker(time.Second * 3)
				for range ticker.C {
					this.peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: track.SSRC()}})
				}
			}()

			//this.videoTrack = nTrack
			this.rewriteRTP(track, this.videoTrack)
		case webrtc.RTPCodecTypeAudio:
			//this.audioTrack = nTrack
			this.rewrite(track, this.audioTrack)
		}
	})

	if err = this.peer.SetRemoteDescription(*remoteSession); err != nil {
		return err
	}

	answer, err := this.peer.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err = this.peer.SetLocalDescription(answer); err != nil {
		return err
	}
	return nil
}

func (this *Router) addSub(subscriber string, remoteSession *webrtc.SessionDescription) (peer *webrtc.PeerConnection, err error) {
	if peer, err = this.wtAPI.NewPeerConnection(*this.wtConfig); err != nil {
		return nil, err
	}
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed {
			this.mu.Lock()
			var sub = this.subs[subscriber]
			delete(this.subs, subscriber)
			this.mu.Unlock()

			if sub != nil {
				sub.Close()
			}
		}
	})
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
	})

	if this.videoTrack != nil {
		peer.AddTrack(this.videoTrack)
	} else {
		log4go.Println("ssss")
	}
	if this.audioTrack != nil {
		peer.AddTrack(this.audioTrack)
	} else {
		log4go.Println("eeee")
	}

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
	return peer, nil
}

func (this *Router) Subscribe(subscriber string, remoteSession *webrtc.SessionDescription) (localSession *webrtc.SessionDescription, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()

	peer := this.subs[subscriber]
	if peer != nil {
		peer.Close()
	}

	peer, err = this.addSub(subscriber, remoteSession)
	if err != nil {
		return nil, err
	}

	this.subs[subscriber] = peer

	return peer.LocalDescription(), nil
}

func (this *Router) Unsubscribe(subscriber string) {
	this.mu.Lock()
	defer this.mu.Unlock()
	var peer = this.subs[subscriber]
	if peer != nil {
		peer.Close()
	}
}

func (this *Router) rewrite(src, dst *webrtc.Track) error {
	var rtpBuf = make([]byte, 1460)
	var i int
	var err error
	for {
		i, err = src.Read(rtpBuf)
		if err != nil {
			return err
		}
		_, err = dst.Write(rtpBuf[:i])
		if err != nil && err != io.ErrClosedPipe {
			return err
		}
	}
	return nil
}

func (this *Router) rewriteRTP(src, dst *webrtc.Track) error {
	var err error
	var packet *rtp.Packet
	for {
		packet, err = src.ReadRTP()
		if err != nil {
			return err
		}
		packet.SSRC = dst.SSRC()
		err = dst.WriteRTP(packet)
		if err != nil && err != io.ErrClosedPipe {
			return err
		}
	}
	return nil
}

func (this *Router) LocalDescription() *webrtc.SessionDescription {
	if this.peer != nil {
		return this.peer.LocalDescription()
	}
	return nil
}

func (this *Router) Close() error {
	this.mu.Lock()
	this.peer.Close()
	for key, sub := range this.subs {
		sub.Close()
		delete(this.subs, key)
	}
	this.mu.Unlock()
	return nil
}