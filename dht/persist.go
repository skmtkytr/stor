package dht

import (
	"fmt"
	"os"
)

// SaveNodes writes the routing table nodes to a file in compact format (26 bytes per node).
func (d *DHT) SaveNodes(path string) error {
	nodes := d.table.AllNodes()
	if len(nodes) == 0 {
		return nil
	}

	compact := make([]CompactNodeInfo, len(nodes))
	for i, n := range nodes {
		compact[i] = CompactNodeInfo{ID: n.ID, Addr: n.Addr}
	}

	data := MarshalCompactNodeInfo(compact)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("dht: save nodes: %w", err)
	}
	return nil
}

// LoadNodes reads nodes from a file and adds them to the routing table.
// Returns the number of nodes loaded.
func (d *DHT) LoadNodes(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("dht: load nodes: %w", err)
	}
	if len(data) == 0 {
		return 0, nil
	}

	nodes, err := UnmarshalCompactNodeInfo(data)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, ni := range nodes {
		d.table.Update(&Node{ID: ni.ID, Addr: ni.Addr})
		count++
	}
	return count, nil
}
