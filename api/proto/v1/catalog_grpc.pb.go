// Code generated from api/proto/v1/catalog.proto; DO NOT EDIT MANUALLY WITHOUT SYNCING catalog.proto.

package catalogv1

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	CatalogService_CreateKnowledgeBase_FullMethodName = "/rag.catalog.v1.CatalogService/CreateKnowledgeBase"
	CatalogService_UpdateKnowledgeBase_FullMethodName = "/rag.catalog.v1.CatalogService/UpdateKnowledgeBase"
	CatalogService_GetKnowledgeBase_FullMethodName    = "/rag.catalog.v1.CatalogService/GetKnowledgeBase"
	CatalogService_ListKnowledgeBases_FullMethodName  = "/rag.catalog.v1.CatalogService/ListKnowledgeBases"
	CatalogService_DeleteKnowledgeBase_FullMethodName = "/rag.catalog.v1.CatalogService/DeleteKnowledgeBase"
	CatalogService_TriggerReindexing_FullMethodName   = "/rag.catalog.v1.CatalogService/TriggerReindexing"
	CatalogService_PromoteIndexPointer_FullMethodName = "/rag.catalog.v1.CatalogService/PromoteIndexPointer"
	CatalogService_AbortReindexing_FullMethodName     = "/rag.catalog.v1.CatalogService/AbortReindexing"
)

// CatalogServiceClient is the client API for CatalogService service.
type CatalogServiceClient interface {
	CreateKnowledgeBase(ctx context.Context, in *CreateKBRequest, opts ...grpc.CallOption) (*KBResponse, error)
	UpdateKnowledgeBase(ctx context.Context, in *UpdateKBRequest, opts ...grpc.CallOption) (*KBResponse, error)
	GetKnowledgeBase(ctx context.Context, in *GetKBRequest, opts ...grpc.CallOption) (*KBResponse, error)
	ListKnowledgeBases(ctx context.Context, in *ListKBsRequest, opts ...grpc.CallOption) (*ListKBsResponse, error)
	DeleteKnowledgeBase(ctx context.Context, in *DeleteKBRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
	TriggerReindexing(ctx context.Context, in *TriggerReindexingRequest, opts ...grpc.CallOption) (*MigrationResponse, error)
	PromoteIndexPointer(ctx context.Context, in *PromotePointerRequest, opts ...grpc.CallOption) (*KBResponse, error)
	AbortReindexing(ctx context.Context, in *AbortReindexingRequest, opts ...grpc.CallOption) (*KBResponse, error)
}

type catalogServiceClient struct {
	cc grpc.ClientConnInterface
}

// NewCatalogServiceClient creates a gRPC client for rag.catalog.v1.CatalogService.
func NewCatalogServiceClient(cc grpc.ClientConnInterface) CatalogServiceClient {
	return &catalogServiceClient{cc}
}

func (c *catalogServiceClient) CreateKnowledgeBase(ctx context.Context, in *CreateKBRequest, opts ...grpc.CallOption) (*KBResponse, error) {
	out := new(KBResponse)
	err := c.cc.Invoke(ctx, CatalogService_CreateKnowledgeBase_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *catalogServiceClient) UpdateKnowledgeBase(ctx context.Context, in *UpdateKBRequest, opts ...grpc.CallOption) (*KBResponse, error) {
	out := new(KBResponse)
	err := c.cc.Invoke(ctx, CatalogService_UpdateKnowledgeBase_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *catalogServiceClient) GetKnowledgeBase(ctx context.Context, in *GetKBRequest, opts ...grpc.CallOption) (*KBResponse, error) {
	out := new(KBResponse)
	err := c.cc.Invoke(ctx, CatalogService_GetKnowledgeBase_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *catalogServiceClient) ListKnowledgeBases(ctx context.Context, in *ListKBsRequest, opts ...grpc.CallOption) (*ListKBsResponse, error) {
	out := new(ListKBsResponse)
	err := c.cc.Invoke(ctx, CatalogService_ListKnowledgeBases_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *catalogServiceClient) DeleteKnowledgeBase(ctx context.Context, in *DeleteKBRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, CatalogService_DeleteKnowledgeBase_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *catalogServiceClient) TriggerReindexing(ctx context.Context, in *TriggerReindexingRequest, opts ...grpc.CallOption) (*MigrationResponse, error) {
	out := new(MigrationResponse)
	err := c.cc.Invoke(ctx, CatalogService_TriggerReindexing_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *catalogServiceClient) PromoteIndexPointer(ctx context.Context, in *PromotePointerRequest, opts ...grpc.CallOption) (*KBResponse, error) {
	out := new(KBResponse)
	err := c.cc.Invoke(ctx, CatalogService_PromoteIndexPointer_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *catalogServiceClient) AbortReindexing(ctx context.Context, in *AbortReindexingRequest, opts ...grpc.CallOption) (*KBResponse, error) {
	out := new(KBResponse)
	err := c.cc.Invoke(ctx, CatalogService_AbortReindexing_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CatalogServiceServer is the server API for CatalogService service.
type CatalogServiceServer interface {
	CreateKnowledgeBase(context.Context, *CreateKBRequest) (*KBResponse, error)
	UpdateKnowledgeBase(context.Context, *UpdateKBRequest) (*KBResponse, error)
	GetKnowledgeBase(context.Context, *GetKBRequest) (*KBResponse, error)
	ListKnowledgeBases(context.Context, *ListKBsRequest) (*ListKBsResponse, error)
	DeleteKnowledgeBase(context.Context, *DeleteKBRequest) (*emptypb.Empty, error)
	TriggerReindexing(context.Context, *TriggerReindexingRequest) (*MigrationResponse, error)
	PromoteIndexPointer(context.Context, *PromotePointerRequest) (*KBResponse, error)
	AbortReindexing(context.Context, *AbortReindexingRequest) (*KBResponse, error)
	mustEmbedUnimplementedCatalogServiceServer()
}

// UnimplementedCatalogServiceServer must be embedded to have forward compatible implementations.
type UnimplementedCatalogServiceServer struct{}

func (UnimplementedCatalogServiceServer) CreateKnowledgeBase(context.Context, *CreateKBRequest) (*KBResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateKnowledgeBase not implemented")
}
func (UnimplementedCatalogServiceServer) UpdateKnowledgeBase(context.Context, *UpdateKBRequest) (*KBResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateKnowledgeBase not implemented")
}
func (UnimplementedCatalogServiceServer) GetKnowledgeBase(context.Context, *GetKBRequest) (*KBResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetKnowledgeBase not implemented")
}
func (UnimplementedCatalogServiceServer) ListKnowledgeBases(context.Context, *ListKBsRequest) (*ListKBsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListKnowledgeBases not implemented")
}
func (UnimplementedCatalogServiceServer) DeleteKnowledgeBase(context.Context, *DeleteKBRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DeleteKnowledgeBase not implemented")
}
func (UnimplementedCatalogServiceServer) TriggerReindexing(context.Context, *TriggerReindexingRequest) (*MigrationResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method TriggerReindexing not implemented")
}
func (UnimplementedCatalogServiceServer) PromoteIndexPointer(context.Context, *PromotePointerRequest) (*KBResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method PromoteIndexPointer not implemented")
}
func (UnimplementedCatalogServiceServer) AbortReindexing(context.Context, *AbortReindexingRequest) (*KBResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method AbortReindexing not implemented")
}
func (UnimplementedCatalogServiceServer) mustEmbedUnimplementedCatalogServiceServer() {}

// RegisterCatalogServiceServer registers the CatalogServiceServer with a grpc.ServiceRegistrar.
func RegisterCatalogServiceServer(s grpc.ServiceRegistrar, srv CatalogServiceServer) {
	s.RegisterService(&CatalogService_ServiceDesc, srv)
}

func _CatalogService_CreateKnowledgeBase_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(CreateKBRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(CatalogServiceServer).CreateKnowledgeBase(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: CatalogService_CreateKnowledgeBase_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(CatalogServiceServer).CreateKnowledgeBase(ctx, req.(*CreateKBRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _CatalogService_UpdateKnowledgeBase_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(UpdateKBRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(CatalogServiceServer).UpdateKnowledgeBase(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: CatalogService_UpdateKnowledgeBase_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(CatalogServiceServer).UpdateKnowledgeBase(ctx, req.(*UpdateKBRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _CatalogService_GetKnowledgeBase_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(GetKBRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(CatalogServiceServer).GetKnowledgeBase(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: CatalogService_GetKnowledgeBase_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(CatalogServiceServer).GetKnowledgeBase(ctx, req.(*GetKBRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _CatalogService_ListKnowledgeBases_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(ListKBsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(CatalogServiceServer).ListKnowledgeBases(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: CatalogService_ListKnowledgeBases_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(CatalogServiceServer).ListKnowledgeBases(ctx, req.(*ListKBsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _CatalogService_DeleteKnowledgeBase_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(DeleteKBRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(CatalogServiceServer).DeleteKnowledgeBase(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: CatalogService_DeleteKnowledgeBase_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(CatalogServiceServer).DeleteKnowledgeBase(ctx, req.(*DeleteKBRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _CatalogService_TriggerReindexing_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(TriggerReindexingRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(CatalogServiceServer).TriggerReindexing(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: CatalogService_TriggerReindexing_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(CatalogServiceServer).TriggerReindexing(ctx, req.(*TriggerReindexingRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _CatalogService_PromoteIndexPointer_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(PromotePointerRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(CatalogServiceServer).PromoteIndexPointer(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: CatalogService_PromoteIndexPointer_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(CatalogServiceServer).PromoteIndexPointer(ctx, req.(*PromotePointerRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _CatalogService_AbortReindexing_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(AbortReindexingRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(CatalogServiceServer).AbortReindexing(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: CatalogService_AbortReindexing_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(CatalogServiceServer).AbortReindexing(ctx, req.(*AbortReindexingRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// CatalogService_ServiceDesc is the grpc.ServiceDesc for CatalogService service.
var CatalogService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "rag.catalog.v1.CatalogService",
	HandlerType: (*CatalogServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "CreateKnowledgeBase",
			Handler:    _CatalogService_CreateKnowledgeBase_Handler,
		},
		{
			MethodName: "UpdateKnowledgeBase",
			Handler:    _CatalogService_UpdateKnowledgeBase_Handler,
		},
		{
			MethodName: "GetKnowledgeBase",
			Handler:    _CatalogService_GetKnowledgeBase_Handler,
		},
		{
			MethodName: "ListKnowledgeBases",
			Handler:    _CatalogService_ListKnowledgeBases_Handler,
		},
		{
			MethodName: "DeleteKnowledgeBase",
			Handler:    _CatalogService_DeleteKnowledgeBase_Handler,
		},
		{
			MethodName: "TriggerReindexing",
			Handler:    _CatalogService_TriggerReindexing_Handler,
		},
		{
			MethodName: "PromoteIndexPointer",
			Handler:    _CatalogService_PromoteIndexPointer_Handler,
		},
		{
			MethodName: "AbortReindexing",
			Handler:    _CatalogService_AbortReindexing_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "api/proto/v1/catalog.proto",
}
