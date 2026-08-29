package vpn

import (
	"sync"

	"flexconnect/internal/anyconnect/proto"
)

const (
	minimumPacketMTU = 576
	maximumPacketMTU = 9000
	packetHeadroom   = 256
)

var payloadPools sync.Map

func payloadBufferSize(mtu int) int {
	if mtu < minimumPacketMTU {
		mtu = 1399
	}
	if mtu > maximumPacketMTU {
		mtu = maximumPacketMTU
	}
	return mtu + packetHeadroom
}

func poolForSize(size int) *sync.Pool {
	value, _ := payloadPools.LoadOrStore(size, &sync.Pool{New: func() any {
		return &proto.Payload{Data: make([]byte, size)}
	}})
	return value.(*sync.Pool)
}

func getPayloadBuffer(mtu ...int) *proto.Payload {
	value := 0
	if len(mtu) > 0 {
		value = mtu[0]
	}
	size := payloadBufferSize(value)
	pl := poolForSize(size).Get().(*proto.Payload)
	pl.Data = pl.Data[:size]
	return pl
}

func putPayloadBuffer(pl *proto.Payload) {
	if pl == nil || cap(pl.Data) < minimumPacketMTU+packetHeadroom || cap(pl.Data) > maximumPacketMTU+packetHeadroom {
		return
	}
	size := cap(pl.Data)
	pl.Type = 0
	pl.Data = pl.Data[:size]
	poolForSize(size).Put(pl)
}
