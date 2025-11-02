package grpc

import (
	"context"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"
)

func PutProtoMessage(ctx context.Context, cli *clientv3.Client, key string, msg proto.Message) (*clientv3.PutResponse, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return cli.Put(ctx, key, string(data))
}

func GetProtoMessage[T proto.Message](ctx context.Context, cli *clientv3.Client, key string, msg T) (T, *clientv3.GetResponse, error) {
	resp, err := cli.Get(ctx, key)
	if err != nil {
		return msg, nil, err
	}
	if len(resp.Kvs) == 0 {
		return msg, resp, fmt.Errorf("not found: %s", key)
	}
	if unMarshalErr := proto.Unmarshal(resp.Kvs[0].Value, msg); unMarshalErr != nil {
		return msg, resp, unMarshalErr
	}
	return msg, resp, nil
}
