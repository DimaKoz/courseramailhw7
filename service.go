package main

// тут вы пишете код
// обращаю ваше внимание - в этом задании запрещены глобальные переменные

import (
	context "context" //this import from the code generation
	"encoding/json"
	"fmt"
	"github.com/golang/protobuf/proto"
	grpc "google.golang.org/grpc" //this import from the code generation
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"log"
	"math"
	"net"
	"strings"
	"sync"
	"time"
)

//MsCtx represents data which uses by this microservice
type MsCtx struct {
	Acl      map[string][]string
	Lock     *sync.Mutex
	Loggers  map[int]chan *Event
	StatData map[int]Stat
}

//Check is an implementation Check function of BizServer interface
//Just a stub
func (m MsCtx) Check(ctx context.Context, nothing *Nothing) (*Nothing, error) {
	log.Println("Check")
	return nothing, nil
}

//Add is an implementation Add function of BizServer interface
//Just a stub
func (m MsCtx) Add(ctx context.Context, nothing *Nothing) (*Nothing, error) {
	log.Println("Add")
	return nothing, nil
}

//Test is an implementation Test function of BizServer interface
//Just a stub
func (m MsCtx) Test(ctx context.Context, nothing *Nothing) (*Nothing, error) {
	log.Println("Test")
	return nothing, nil
}

//Logging is an implementation Logging function of AdminServer interface
func (m *MsCtx) Logging(nothing *Nothing, server Admin_LoggingServer) error {
	log.Println("Logging")
	id, channel := m.addLogger()
	log.Printf("Logger %d added \n", id)
	for {
		msg := <-channel
		log.Printf("sending to logger %v a message %#v \n", id, msg)
		err := server.Send(msg)
		if err != nil {
			return err
		}
	}
}

//logEvent sends notification to loggers
func (m *MsCtx) logEvent(consumer string, method string, host string) {
	log.Printf("logEvent consumer %s method %s \n", consumer, method)
	m.Lock.Lock()
	defer m.Lock.Unlock()
	for logger, c := range m.Loggers {
		fmt.Printf("Notification to logger %v\n", logger)
		c <- &Event{Timestamp: time.Now().UnixNano(), Consumer: consumer, Method: method, Host: host}
	}

}

//Statistics is an implementation Statistics function of AdminServer interface
func (m MsCtx) Statistics(interval *StatInterval, server Admin_StatisticsServer) error {
	log.Println("Statistics")
	sec := interval.IntervalSeconds
	ticker := time.NewTicker(time.Duration(sec) * time.Second)
	clientId := m.addStatClient()
	for {
		<-ticker.C
		stat := m.getStat(clientId)
		err := server.Send(&stat)
		m.clearStat(clientId)
		if err != nil {
			m.deleteStatClient(clientId)
			return err
		}
	}
}

func (m *MsCtx) addStatClient() int {
	m.Lock.Lock()
	number := len(m.StatData)
	m.StatData[number] = Stat{ByConsumer: make(map[string]uint64), ByMethod: make(map[string]uint64)}
	m.Lock.Unlock()
	return number
}

func (m *MsCtx) deleteStatClient(client int) {
	m.Lock.Lock()
	delete(m.StatData, client)
	m.Lock.Unlock()
}

func (m *MsCtx) clearStat(number int) {
	m.Lock.Lock()
	m.StatData[number] = Stat{ByConsumer: make(map[string]uint64), ByMethod: make(map[string]uint64)}
	m.Lock.Unlock()
}

func (m *MsCtx) getStat(client int) Stat {
	m.Lock.Lock()
	defer m.Lock.Unlock()
	s := m.StatData[client]
	s.Timestamp = time.Now().UnixNano()
	return s
}

func (m *MsCtx) addUsageStat(consumer string, method string) {
	m.Lock.Lock()
	for _, stat := range m.StatData {
		stat.ByMethod[method]++
		stat.ByConsumer[consumer]++
	}
	m.Lock.Unlock()
}

//NewMsCtx helps to make a MsCtx
func NewMsCtx() *MsCtx {
	result := &MsCtx{}
	result.Lock = &sync.Mutex{}
	result.Loggers = make(map[int]chan *Event)
	result.StatData = make(map[int]Stat)
	return result
}

//addLogger adds a logger to MsCtx and returns a number of the added logger and a created channel of the logger
func (m *MsCtx) addLogger() (int, chan *Event) {
	m.Lock.Lock()
	defer m.Lock.Unlock()
	count := len(m.Loggers)
	m.Loggers[count] = make(chan *Event)
	return count, m.Loggers[count]
}

//isConsumerAllowed returns true if a consumer and a method are allowed
func (m MsCtx) isConsumerAllowed(consumer string, checkingMethod string) bool {
	methods, found := m.Acl[consumer]
	if !found {
		return false
	}
	for _, method := range methods {
		if strings.HasSuffix(method, "*") {
			return true
		}
		if method == checkingMethod {
			return true
		}
	}
	return false
}

func StartMyMicroservice(ctx context.Context, addr string, data string) error {

	msCtx := NewMsCtx()
	err := json.Unmarshal([]byte(data), &msCtx.Acl)
	if err != nil {
		return err
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Println("can't listen a port:", addr, err)
		return err
	}

	server := grpc.NewServer(
		grpc.UnaryInterceptor(unaryInterceptor),
		grpc.StreamInterceptor(streamInterceptor),
	)

	RegisterAdminServer(server, msCtx)
	RegisterBizServer(server, msCtx)

	go func() {
		select {
		case <-ctx.Done():
			log.Println("closing server")
			server.GracefulStop()
		}
	}()

	go func() {
		log.Println("starting server at " + addr)
		server.Serve(lis)
	}()

	return err
}

//unaryInterceptor presents an unary interceptor for grpc
func unaryInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	start := time.Now()

	if _, err := checkRights(ctx, info.Server, info.FullMethod); err != nil {
		return nil, err
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		log.Println("metadata.FromIncomingContext(ctx) is !ok")
		return nil, status.Error(codes.Internal, "internal error")
	}
	consumer, err := getConsumer(md)
	if err != nil {
		return nil, err
	}

	p, ok := peer.FromContext(ctx)
	if !ok {
		log.Println("peer.FromContext(ctx) is !ok")
		return nil, status.Error(codes.Internal, "internal error")
	}
	host := p.Addr.String()
	msCtx := info.Server.(*MsCtx)
	msCtx.logEvent(consumer, info.FullMethod, host)
	msCtx.addUsageStat(consumer, info.FullMethod)
	reply, err := handler(ctx, req)

	log.Printf(`--
	after incoming call=%v
	req=%#v
	reply=%#v
	time=%v
	err=%v
`, info.FullMethod, req, reply, time.Since(start), err)
	return reply, err
}

//streamInterceptor presents an stream interceptor for grpc
func streamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	start := time.Now()

	if _, err := checkRights(ss.Context(), srv, info.FullMethod); err != nil {
		return err
	}

	md, _ := metadata.FromIncomingContext(ss.Context())
	consumer, err := getConsumer(md)
	if err != nil {
		return err
	}
	//host is "127.0.0.1:"
	p, ok := peer.FromContext(ss.Context())
	if !ok {
		log.Println("peer.FromContext(ctx) is !ok")
		return status.Error(codes.Internal, "internal error")
	}
	host := p.Addr.String()
	msCtx := srv.(*MsCtx)
	msCtx.logEvent(consumer, info.FullMethod, host)
	msCtx.addUsageStat(consumer, info.FullMethod)

	err = handler(srv, ss)
	log.Printf(`--
	after incoming call=%v
	req=%#v
	time=%v
	err=%v
`, info.FullMethod, srv, time.Since(start), err)
	return err
}

//getConsumer returns a consumer from metadata.MD or error
func getConsumer(md metadata.MD) (string, error) {

	consumers, ok := md["consumer"]
	err := status.Error(codes.Unauthenticated, "no consumer from you")
	if !ok {
		return "", err
	}

	if len(consumers) == 0 {
		return "", err
	}

	return consumers[0], nil

}

func checkRights(ctx context.Context, srv interface{}, method string) (bool, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		log.Println("metadata.FromIncomingContext(ctx) has !ok")
		return false, status.Error(codes.Internal, "internal error")
	}
	consumer, ok := md["consumer"]
	log.Printf(`--checkRights 
	consumer=%v
	ok=%#v
	md=%v
`, consumer, ok, md)

	if !ok {
		return false, status.Error(codes.Unauthenticated, "no consumer from you")
	}

	if len(consumer) == 0 {
		return false, status.Error(codes.Unauthenticated, "no consumer from you")
	}

	msCtx, ok := srv.(*MsCtx)
	if !ok {
		log.Println("srv.(*MsCtx) has !ok")
		return false, status.Error(codes.Internal, "internal error")
	}

	hasRight := msCtx.isConsumerAllowed(consumer[0], method)
	if !hasRight {
		return false, status.Error(codes.Unauthenticated, fmt.Sprintf("no rights for '%s'", consumer[0]))
	}
	return true, nil
}

/*Below is generated code*/

// Code generated by protoc-gen-go. DO NOT EDIT.

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.ProtoPackageIsVersion2 // please upgrade the proto package

type Event struct {
	Timestamp int64  `protobuf:"varint,1,opt,name=timestamp" json:"timestamp,omitempty"`
	Consumer  string `protobuf:"bytes,2,opt,name=consumer" json:"consumer,omitempty"`
	Method    string `protobuf:"bytes,3,opt,name=method" json:"method,omitempty"`
	Host      string `protobuf:"bytes,4,opt,name=host" json:"host,omitempty"`
}

func (m *Event) Reset()                    { *m = Event{} }
func (m *Event) String() string            { return proto.CompactTextString(m) }
func (*Event) ProtoMessage()               {}
func (*Event) Descriptor() ([]byte, []int) { return fileDescriptor0, []int{0} }

func (m *Event) GetTimestamp() int64 {
	if m != nil {
		return m.Timestamp
	}
	return 0
}

func (m *Event) GetConsumer() string {
	if m != nil {
		return m.Consumer
	}
	return ""
}

func (m *Event) GetMethod() string {
	if m != nil {
		return m.Method
	}
	return ""
}

func (m *Event) GetHost() string {
	if m != nil {
		return m.Host
	}
	return ""
}

type Stat struct {
	Timestamp  int64             `protobuf:"varint,1,opt,name=timestamp" json:"timestamp,omitempty"`
	ByMethod   map[string]uint64 `protobuf:"bytes,2,rep,name=by_method,json=byMethod" json:"by_method,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"varint,2,opt,name=value"`
	ByConsumer map[string]uint64 `protobuf:"bytes,3,rep,name=by_consumer,json=byConsumer" json:"by_consumer,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"varint,2,opt,name=value"`
}

func (x *Stat) Reset()                    { *x = Stat{} }
func (x *Stat) String() string            { return proto.CompactTextString(x) }
func (*Stat) ProtoMessage()               {}
func (*Stat) Descriptor() ([]byte, []int) { return fileDescriptor0, []int{1} }

func (x *Stat) GetTimestamp() int64 {
	if x != nil {
		return x.Timestamp
	}
	return 0
}

func (x *Stat) GetByMethod() map[string]uint64 {
	if x != nil {
		return x.ByMethod
	}
	return nil
}

func (x *Stat) GetByConsumer() map[string]uint64 {
	if x != nil {
		return x.ByConsumer
	}
	return nil
}

type StatInterval struct {
	IntervalSeconds uint64 `protobuf:"varint,1,opt,name=interval_seconds,json=intervalSeconds" json:"interval_seconds,omitempty"`
}

func (x *StatInterval) Reset()                    { *x = StatInterval{} }
func (x *StatInterval) String() string            { return proto.CompactTextString(x) }
func (*StatInterval) ProtoMessage()               {}
func (*StatInterval) Descriptor() ([]byte, []int) { return fileDescriptor0, []int{2} }

func (x *StatInterval) GetIntervalSeconds() uint64 {
	if x != nil {
		return x.IntervalSeconds
	}
	return 0
}

type Nothing struct {
	Dummy bool `protobuf:"varint,1,opt,name=dummy" json:"dummy,omitempty"`
}

func (x *Nothing) Reset()                    { *x = Nothing{} }
func (x *Nothing) String() string            { return proto.CompactTextString(x) }
func (*Nothing) ProtoMessage()               {}
func (*Nothing) Descriptor() ([]byte, []int) { return fileDescriptor0, []int{3} }

func (x *Nothing) GetDummy() bool {
	if x != nil {
		return x.Dummy
	}
	return false
}

func init() {
	proto.RegisterType((*Event)(nil), "main.Event")
	proto.RegisterType((*Stat)(nil), "main.Stat")
	proto.RegisterType((*StatInterval)(nil), "main.StatInterval")
	proto.RegisterType((*Nothing)(nil), "main.Nothing")
}

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ grpc.ClientConn

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
const _ = grpc.SupportPackageIsVersion4

// Client API for Admin service

type AdminClient interface {
	Logging(ctx context.Context, in *Nothing, opts ...grpc.CallOption) (Admin_LoggingClient, error)
	Statistics(ctx context.Context, in *StatInterval, opts ...grpc.CallOption) (Admin_StatisticsClient, error)
}

type adminClient struct {
	cc *grpc.ClientConn
}

func NewAdminClient(cc *grpc.ClientConn) AdminClient {
	return &adminClient{cc}
}

func (c *adminClient) Logging(ctx context.Context, in *Nothing, opts ...grpc.CallOption) (Admin_LoggingClient, error) {
	stream, err := grpc.NewClientStream(ctx, &_Admin_serviceDesc.Streams[0], c.cc, "/main.Admin/Logging", opts...)
	if err != nil {
		return nil, err
	}
	x := &adminLoggingClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type Admin_LoggingClient interface {
	Recv() (*Event, error)
	grpc.ClientStream
}

type adminLoggingClient struct {
	grpc.ClientStream
}

func (x *adminLoggingClient) Recv() (*Event, error) {
	m := new(Event)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *adminClient) Statistics(ctx context.Context, in *StatInterval, opts ...grpc.CallOption) (Admin_StatisticsClient, error) {
	stream, err := grpc.NewClientStream(ctx, &_Admin_serviceDesc.Streams[1], c.cc, "/main.Admin/Statistics", opts...)
	if err != nil {
		return nil, err
	}
	x := &adminStatisticsClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type Admin_StatisticsClient interface {
	Recv() (*Stat, error)
	grpc.ClientStream
}

type adminStatisticsClient struct {
	grpc.ClientStream
}

func (x *adminStatisticsClient) Recv() (*Stat, error) {
	m := new(Stat)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// Server API for Admin service

type AdminServer interface {
	Logging(*Nothing, Admin_LoggingServer) error
	Statistics(*StatInterval, Admin_StatisticsServer) error
}

func RegisterAdminServer(s *grpc.Server, srv AdminServer) {
	s.RegisterService(&_Admin_serviceDesc, srv)
}

func _Admin_Logging_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(Nothing)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(AdminServer).Logging(m, &adminLoggingServer{stream})
}

type Admin_LoggingServer interface {
	Send(*Event) error
	grpc.ServerStream
}

type adminLoggingServer struct {
	grpc.ServerStream
}

func (x *adminLoggingServer) Send(m *Event) error {
	return x.ServerStream.SendMsg(m)
}

func _Admin_Statistics_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(StatInterval)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(AdminServer).Statistics(m, &adminStatisticsServer{stream})
}

type Admin_StatisticsServer interface {
	Send(*Stat) error
	grpc.ServerStream
}

type adminStatisticsServer struct {
	grpc.ServerStream
}

func (x *adminStatisticsServer) Send(m *Stat) error {
	return x.ServerStream.SendMsg(m)
}

var _Admin_serviceDesc = grpc.ServiceDesc{
	ServiceName: "main.Admin",
	HandlerType: (*AdminServer)(nil),
	Methods:     []grpc.MethodDesc{},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Logging",
			Handler:       _Admin_Logging_Handler,
			ServerStreams: true,
		},
		{
			StreamName:    "Statistics",
			Handler:       _Admin_Statistics_Handler,
			ServerStreams: true,
		},
	},
	Metadata: "service.proto",
}

// Client API for Biz service

type BizClient interface {
	Check(ctx context.Context, in *Nothing, opts ...grpc.CallOption) (*Nothing, error)
	Add(ctx context.Context, in *Nothing, opts ...grpc.CallOption) (*Nothing, error)
	Test(ctx context.Context, in *Nothing, opts ...grpc.CallOption) (*Nothing, error)
}

type bizClient struct {
	cc *grpc.ClientConn
}

func NewBizClient(cc *grpc.ClientConn) BizClient {
	return &bizClient{cc}
}

func (c *bizClient) Check(ctx context.Context, in *Nothing, opts ...grpc.CallOption) (*Nothing, error) {
	out := new(Nothing)
	err := grpc.Invoke(ctx, "/main.Biz/Check", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *bizClient) Add(ctx context.Context, in *Nothing, opts ...grpc.CallOption) (*Nothing, error) {
	out := new(Nothing)
	err := grpc.Invoke(ctx, "/main.Biz/Add", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *bizClient) Test(ctx context.Context, in *Nothing, opts ...grpc.CallOption) (*Nothing, error) {
	out := new(Nothing)
	err := grpc.Invoke(ctx, "/main.Biz/Test", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Server API for Biz service

type BizServer interface {
	Check(context.Context, *Nothing) (*Nothing, error)
	Add(context.Context, *Nothing) (*Nothing, error)
	Test(context.Context, *Nothing) (*Nothing, error)
}

func RegisterBizServer(s *grpc.Server, srv BizServer) {
	s.RegisterService(&_Biz_serviceDesc, srv)
}

func _Biz_Check_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(Nothing)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BizServer).Check(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/main.Biz/Check",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BizServer).Check(ctx, req.(*Nothing))
	}
	return interceptor(ctx, in, info, handler)
}

func _Biz_Add_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(Nothing)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BizServer).Add(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/main.Biz/Add",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BizServer).Add(ctx, req.(*Nothing))
	}
	return interceptor(ctx, in, info, handler)
}

func _Biz_Test_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(Nothing)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(BizServer).Test(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/main.Biz/Test",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(BizServer).Test(ctx, req.(*Nothing))
	}
	return interceptor(ctx, in, info, handler)
}

var _Biz_serviceDesc = grpc.ServiceDesc{
	ServiceName: "main.Biz",
	HandlerType: (*BizServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Check",
			Handler:    _Biz_Check_Handler,
		},
		{
			MethodName: "Add",
			Handler:    _Biz_Add_Handler,
		},
		{
			MethodName: "Test",
			Handler:    _Biz_Test_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "service.proto",
}

func init() { proto.RegisterFile("service.proto", fileDescriptor0) }

var fileDescriptor0 = []byte{
	// 386 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0x94, 0x52, 0x5d, 0xab, 0xda, 0x40,
	0x10, 0xbd, 0xf9, 0xba, 0xd7, 0x8c, 0x95, 0x7b, 0x19, 0x4a, 0x09, 0xa1, 0x50, 0x09, 0xb4, 0xf5,
	0xbe, 0x04, 0xb1, 0x14, 0xda, 0x4a, 0x1f, 0x54, 0x7c, 0x28, 0xb4, 0x7d, 0x88, 0x7d, 0x97, 0x7c,
	0x2c, 0x66, 0xd1, 0xdd, 0x95, 0xec, 0x1a, 0x48, 0xa1, 0xff, 0xa2, 0x3f, 0xb8, 0xec, 0x26, 0x2a,
	0xfa, 0x22, 0x7d, 0x9b, 0x73, 0x66, 0xce, 0x99, 0xc3, 0x30, 0x30, 0x90, 0xa4, 0xaa, 0x69, 0x4e,
	0xe2, 0x7d, 0x25, 0x94, 0x40, 0x97, 0xa5, 0x94, 0x47, 0x0c, 0xbc, 0x65, 0x4d, 0xb8, 0xc2, 0xd7,
	0xe0, 0x2b, 0xca, 0x88, 0x54, 0x29, 0xdb, 0x07, 0xd6, 0xd0, 0x1a, 0x39, 0xc9, 0x99, 0xc0, 0x10,
	0x7a, 0xb9, 0xe0, 0xf2, 0xc0, 0x48, 0x15, 0xd8, 0x43, 0x6b, 0xe4, 0x27, 0x27, 0x8c, 0xaf, 0xe0,
	0x9e, 0x11, 0x55, 0x8a, 0x22, 0x70, 0x4c, 0xa7, 0x43, 0x88, 0xe0, 0x96, 0x42, 0xaa, 0xc0, 0x35,
	0xac, 0xa9, 0xa3, 0xbf, 0x36, 0xb8, 0x2b, 0x95, 0xde, 0x5a, 0xf7, 0x11, 0xfc, 0xac, 0x59, 0x77,
	0xae, 0xf6, 0xd0, 0x19, 0xf5, 0x27, 0x41, 0xac, 0xf3, 0xc6, 0x5a, 0x1c, 0xcf, 0x9b, 0x1f, 0xa6,
	0xb5, 0xe4, 0xaa, 0x6a, 0x92, 0x5e, 0xd6, 0x41, 0x9c, 0x42, 0x3f, 0x6b, 0xd6, 0xa7, 0xa0, 0x8e,
	0x11, 0x86, 0x17, 0xc2, 0x45, 0xd7, 0x6c, 0xa5, 0x90, 0x9d, 0x88, 0x70, 0x0a, 0x83, 0x0b, 0x5f,
	0x7c, 0x02, 0x67, 0x4b, 0x1a, 0x13, 0xce, 0x4f, 0x74, 0x89, 0x2f, 0xc1, 0xab, 0xd3, 0xdd, 0x81,
	0x98, 0x13, 0xb8, 0x49, 0x0b, 0xbe, 0xd8, 0x9f, 0xac, 0xf0, 0x2b, 0x3c, 0x5e, 0x79, 0xff, 0x8f,
	0x3c, 0xfa, 0x0c, 0x2f, 0x74, 0xbe, 0x6f, 0x5c, 0x91, 0xaa, 0x4e, 0x77, 0xf8, 0x0c, 0x4f, 0xb4,
	0xab, 0xd7, 0x92, 0xe4, 0x82, 0x17, 0xd2, 0x18, 0xb9, 0xc9, 0xe3, 0x91, 0x5f, 0xb5, 0x74, 0xf4,
	0x06, 0x1e, 0x7e, 0x0a, 0x55, 0x52, 0xbe, 0xd1, 0xfe, 0xc5, 0x81, 0xb1, 0x76, 0x67, 0x2f, 0x69,
	0xc1, 0xa4, 0x00, 0x6f, 0x56, 0x30, 0xca, 0xf1, 0x19, 0x1e, 0xbe, 0x8b, 0xcd, 0x46, 0x4f, 0x0e,
	0xda, 0x9b, 0x74, 0xc2, 0xb0, 0xdf, 0x42, 0xf3, 0x08, 0xd1, 0xdd, 0xd8, 0xc2, 0x31, 0x80, 0xce,
	0x43, 0xa5, 0xa2, 0xb9, 0x44, 0x3c, 0x5f, 0xf0, 0x98, 0x30, 0x84, 0x33, 0xa7, 0x15, 0x93, 0x3f,
	0xe0, 0xcc, 0xe9, 0x6f, 0x7c, 0x0f, 0xde, 0xa2, 0x24, 0xf9, 0xf6, 0x7a, 0xc3, 0x25, 0x8c, 0xee,
	0xf0, 0x2d, 0x38, 0xb3, 0xa2, 0xb8, 0x39, 0xf6, 0x0e, 0xdc, 0x5f, 0x44, 0xaa, 0x5b, 0x73, 0xd9,
	0xbd, 0xf9, 0xe9, 0x0f, 0xff, 0x02, 0x00, 0x00, 0xff, 0xff, 0x03, 0x1d, 0xb2, 0x19, 0xe4, 0x02,
	0x00, 0x00,
}
