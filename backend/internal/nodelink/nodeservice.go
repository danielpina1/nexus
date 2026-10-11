package nodelink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/gen/nexus/v1/nexusv1connect"
	"github.com/danielpina1/nexus/backend/internal/nodelink/wire"
)

// NodeService implements the client-facing nexusv1connect.NodeServiceHandler.
type NodeService struct{ hub *Hub }

var _ nexusv1connect.NodeServiceHandler = (*NodeService)(nil)

func (s *NodeService) ListNodes(ctx context.Context, _ *connect.Request[nexusv1.ListNodesRequest]) (*connect.Response[nexusv1.ListNodesResponse], error) {
	nodes, err := s.hub.Nodes(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&nexusv1.ListNodesResponse{Nodes: nodes}), nil
}

func (s *NodeService) GetNode(ctx context.Context, req *connect.Request[nexusv1.GetNodeRequest]) (*connect.Response[nexusv1.GetNodeResponse], error) {
	nodes, _ := s.hub.Nodes(ctx)
	for _, n := range nodes {
		if n.Id == req.Msg.Id {
			return connect.NewResponse(&nexusv1.GetNodeResponse{Node: n}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown node %q", req.Msg.Id))
}

// ListFiles asks the node to run its read-only "fs.list" tool via a control
// call (see package wire). Paths are relative to the node's workspaces dir.
func (s *NodeService) ListFiles(ctx context.Context, req *connect.Request[nexusv1.ListFilesRequest]) (*connect.Response[nexusv1.ListFilesResponse], error) {
	if req.Msg.NodeId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("node_id required"))
	}
	in, _ := json.Marshal(map[string]string{"path": req.Msg.Path})
	out, err := s.hub.controlCall(ctx, req.Msg.NodeId, wire.ToolFSList, in)
	if err != nil {
		return nil, err
	}
	var res wire.FSListOutput
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode fs.list output: %w", err))
	}
	resp := &nexusv1.ListFilesResponse{}
	for _, e := range res.Entries {
		fe := &nexusv1.FileEntry{Name: e.Name, Path: e.Path, IsDir: e.IsDir, SizeBytes: e.SizeBytes}
		if t, err := time.Parse(time.RFC3339Nano, e.ModifiedAt); err == nil {
			fe.ModifiedAt = timestamppb.New(t)
		}
		resp.Entries = append(resp.Entries, fe)
	}
	return connect.NewResponse(resp), nil
}

func (s *NodeService) WakeNode(context.Context, *connect.Request[nexusv1.WakeNodeRequest]) (*connect.Response[nexusv1.WakeNodeResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("wake-on-LAN not implemented"))
}
