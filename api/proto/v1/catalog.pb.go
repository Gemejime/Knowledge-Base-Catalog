// Code generated from api/proto/v1/catalog.proto; DO NOT EDIT MANUALLY WITHOUT SYNCING catalog.proto.

package catalogv1

import (
	"encoding/json"

	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/proto"
)

func init() {
	encoding.RegisterCodec(hybridProtoCodec{})
}

type hybridProtoCodec struct{}

func (hybridProtoCodec) Name() string {
	return "proto"
}

func (hybridProtoCodec) Marshal(v any) ([]byte, error) {
	if pm, ok := v.(proto.Message); ok {
		return proto.Marshal(pm)
	}
	return json.Marshal(v)
}

func (hybridProtoCodec) Unmarshal(data []byte, v any) error {
	if pm, ok := v.(proto.Message); ok {
		return proto.Unmarshal(data, pm)
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}
