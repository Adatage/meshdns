package grpcserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/Adatage/meshdns/internal/cockroach"
	"github.com/Adatage/meshdns/internal/config"
	pb "github.com/Adatage/meshdns/pkg/proto"
	"github.com/Adatage/meshdns/internal/zonecache"
)

type Server struct {
	pb.UnimplementedDNSControlServer

	cfg   *config.Config
	db    *cockroach.DB
	zc    *zonecache.Cache
	log   *slog.Logger
	start time.Time

	grpc *grpc.Server
}

func New(cfg *config.Config, db *cockroach.DB, zc *zonecache.Cache, log *slog.Logger) *Server {
	return &Server{
		cfg:   cfg,
		db:    db,
		zc:    zc,
		log:   log,
		start: time.Now(),
	}
}

func (s *Server) Start(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("grpc listen %s: %w", s.cfg.GRPCAddr, err)
	}

	s.grpc = grpc.NewServer()
	pb.RegisterDNSControlServer(s.grpc, s)
	reflection.Register(s.grpc)

	s.log.Info("gRPC control server starting", "addr", s.cfg.GRPCAddr)

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.grpc.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		s.grpc.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) CreateZone(ctx context.Context, req *pb.CreateZoneRequest) (*pb.CreateZoneResponse, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	z, err := s.db.CreateZone(ctx, req.Name)
	if err != nil {
		s.log.Error("CreateZone", "err", err)
		return nil, status.Errorf(codes.Internal, "create zone: %v", err)
	}

	if s.zc != nil {
		s.zc.InvalidateZoneList()
	}

	return &pb.CreateZoneResponse{Zone: zoneToPB(z)}, nil
}

func (s *Server) DeleteZone(ctx context.Context, req *pb.DeleteZoneRequest) (*pb.DeleteZoneResponse, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	if err := s.db.DeleteZone(ctx, req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "delete zone: %v", err)
	}

	if s.zc != nil {
		s.zc.InvalidateZoneList()
		s.zc.InvalidateZone(req.Name)
	}

	return &pb.DeleteZoneResponse{Success: true}, nil
}

func (s *Server) ListZones(ctx context.Context, _ *pb.ListZonesRequest) (*pb.ListZonesResponse, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}

	zones, err := s.db.ListZones(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list zones: %v", err)
	}

	pbZones := make([]*pb.Zone, 0, len(zones))
	for i := range zones {
		pbZones = append(pbZones, zoneToPB(&zones[i]))
	}

	return &pb.ListZonesResponse{Zones: pbZones}, nil
}

func (s *Server) AddRecord(ctx context.Context, req *pb.AddRecordRequest) (*pb.AddRecordResponse, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if req.ZoneName == "" || req.Name == "" || req.Type == "" || req.Data == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_name, name, type and data are required")
	}

	var subnet *string
	if req.Subnet != "" {
		if _, _, err := net.ParseCIDR(req.Subnet); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid subnet CIDR %q: %v", req.Subnet, err)
		}
		s := req.Subnet
		subnet = &s
	}

	rec, err := s.db.AddRecord(ctx, req.ZoneName, req.Name, req.Type, req.Ttl, req.Data, subnet)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "add record: %v", err)
	}

	if s.zc != nil {
		qtype, ok := dns.StringToType[strings.ToUpper(rec.Type)]
		if ok {
			s.zc.InvalidateName(rec.Name, qtype)
		}
	}

	return &pb.AddRecordResponse{Record: recordToPB(rec)}, nil
}

func (s *Server) DeleteRecord(ctx context.Context, req *pb.DeleteRecordRequest) (*pb.DeleteRecordResponse, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	existing, err := s.db.GetRecordByID(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get record: %v", err)
	}

	if err := s.db.DeleteRecord(ctx, req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "delete record: %v", err)
	}

	if s.zc != nil && existing != nil {
		qtype, ok := dns.StringToType[strings.ToUpper(existing.Type)]
		if ok {
			s.zc.InvalidateName(existing.Name, qtype)
		}
	}

	return &pb.DeleteRecordResponse{Success: true}, nil
}

func (s *Server) ListRecords(ctx context.Context, req *pb.ListRecordsRequest) (*pb.ListRecordsResponse, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if req.ZoneName == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_name is required")
	}

	recs, err := s.db.ListRecords(ctx, req.ZoneName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list records: %v", err)
	}

	pbRecs := make([]*pb.Record, 0, len(recs))
	for i := range recs {
		pbRecs = append(pbRecs, recordToPB(&recs[i]))
	}

	return &pb.ListRecordsResponse{Records: pbRecs}, nil
}

func (s *Server) GetStatus(_ context.Context, _ *pb.GetStatusRequest) (*pb.GetStatusResponse, error) {
	resp := &pb.GetStatusResponse{
		RecursiveEnabled:     s.cfg.RecursiveEnabled,
		AuthoritativeEnabled: s.cfg.AuthoritativeEnabled(),
		CacheEnabled:         s.cfg.CacheEnabled(),
		Version:              "1.0.0",
		UptimeSeconds:        int64(time.Since(s.start).Seconds()),
		GrpcAddr:             s.cfg.GRPCAddr,
	}
	if s.cfg.UDPEnabled {
		resp.BindUdp = s.cfg.UDPAddr()
	}
	if s.cfg.TCPEnabled {
		resp.BindTcp = s.cfg.TCPAddr()
	}
	return resp, nil
}

func (s *Server) requireDB() error {
	if s.db == nil {
		return status.Error(codes.Unavailable,
			"authoritative mode is disabled (COCKROACH_DSN not set)")
	}
	return nil
}

func zoneToPB(z *cockroach.Zone) *pb.Zone {
	return &pb.Zone{
		Id:        z.ID,
		Name:      z.Name,
		CreatedAt: z.CreatedAt.Format(time.RFC3339),
		UpdatedAt: z.UpdatedAt.Format(time.RFC3339),
	}
}

func recordToPB(r *cockroach.Record) *pb.Record {
	rec := &pb.Record{
		Id:        r.ID,
		ZoneId:    r.ZoneID,
		ZoneName:  r.ZoneName,
		Name:      r.Name,
		Type:      r.Type,
		Ttl:       r.TTL,
		Data:      r.Data,
		CreatedAt: r.CreatedAt.Format(time.RFC3339),
	}
	if r.Subnet != nil {
		rec.Subnet = *r.Subnet
	}
	return rec
}
