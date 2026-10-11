package nodelink

import (
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/kernel"
)

// nodeStore persists the node registry (optional; used when a store module runs).
type nodeStore struct{ st kernel.Store }

func newNodeStore(st kernel.Store) (*nodeStore, error) {
	err := st.Migrate("nodelink", []string{
		`CREATE TABLE IF NOT EXISTS nodelink_nodes (
			id TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	})
	if err != nil {
		return nil, err
	}
	return &nodeStore{st: st}, nil
}

func (s *nodeStore) upsert(n *nexusv1.Node) error {
	b, err := protojson.Marshal(n)
	if err != nil {
		return err
	}
	_, err = s.st.DB().Exec(`INSERT INTO nodelink_nodes(id, data, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		n.Id, string(b), time.Now().Unix())
	return err
}

func (s *nodeStore) load() ([]*nexusv1.Node, error) {
	rows, err := s.st.DB().Query(`SELECT data FROM nodelink_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*nexusv1.Node
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		n := &nexusv1.Node{}
		if err := protojson.Unmarshal([]byte(data), n); err != nil {
			continue
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
