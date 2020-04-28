package manager

import (
	"fmt"
	"sync"

	"github.com/freakmaxi/kertish-dfs/basics/common"
	"github.com/freakmaxi/kertish-dfs/basics/errors"
	cluster2 "github.com/freakmaxi/kertish-dfs/manager-node/cluster"
	"github.com/freakmaxi/kertish-dfs/manager-node/data"
)

const blockSize uint32 = 1024 * 1024 * 32 // 32Mb

type Cluster interface {
	Register(nodeAddresses []string) (*common.Cluster, error)
	RegisterNodesTo(clusterId string, nodeAddresses []string) error

	UnRegisterCluster(clusterId string) error
	UnRegisterNode(nodeId string) error

	Reserve(size uint64) (*common.ReservationMap, error)
	Commit(reservationId string, clusterMap map[string]uint64) error
	Discard(reservationId string) error
	SyncClusters() error
	SyncCluster(clusterId string) error
	CheckConsistency() error

	GetClusters() (common.Clusters, error)
	GetCluster(clusterId string) (*common.Cluster, error)

	Map(sha512HexList []string, mapType common.MapType) (map[string]string, error)
	Find(sha512Hex string, mapType common.MapType) (string, string, error)
}

type cluster struct {
	clusters data.Clusters
	index    data.Index
	metadata data.Metadata
}

func NewCluster(clusters data.Clusters, index data.Index, metadata data.Metadata) (Cluster, error) {
	return &cluster{
		clusters: clusters,
		index:    index,
		metadata: metadata,
	}, nil
}

func (c *cluster) Register(nodeAddresses []string) (*common.Cluster, error) {
	cluster := common.NewCluster(newClusterId())

	nodes, clusterSize, err := c.prepareNodes(nodeAddresses, 0)
	if err != nil {
		return nil, err
	}
	cluster.Size = clusterSize
	cluster.Nodes = append(cluster.Nodes, nodes...)

	masterAddress := ""
	for i, node := range cluster.Nodes {
		mA := masterAddress

		if i == 0 {
			node.Master = true
			masterAddress = node.Address
		}

		dn, err := cluster2.NewDataNode(node.Address)
		if err != nil {
			return nil, err
		}
		if !dn.Join(cluster.Id, node.Id, mA) {
			return nil, errors.ErrMode
		}
	}

	if err := c.clusters.RegisterCluster(cluster); err != nil {
		return nil, err
	}

	return cluster, nil
}

func (c *cluster) RegisterNodesTo(clusterId string, nodeAddresses []string) error {
	return c.clusters.Save(clusterId, func(cluster *common.Cluster) error {
		masterNode := cluster.Master()

		nodes, _, err := c.prepareNodes(nodeAddresses, cluster.Size)
		if err != nil {
			return err
		}
		cluster.Nodes = append(cluster.Nodes, nodes...)

		for _, node := range nodes {
			dn, err := cluster2.NewDataNode(node.Address)
			if err != nil {
				return err
			}

			if !dn.Join(clusterId, node.Id, masterNode.Address) {
				return errors.ErrJoin
			}
		}

		return nil
	})
}

func (c *cluster) prepareNodes(nodeAddresses []string, clusterSize uint64) (common.NodeList, uint64, error) {
	nodeMap := make(map[string]*common.Node)
	for _, nodeAddress := range nodeAddresses {
		if _, has := nodeMap[nodeAddress]; has {
			return nil, 0, fmt.Errorf("node address entered twice")
		}

		node, err := cluster2.NewDataNode(nodeAddress)
		if err != nil {
			return nil, 0, err
		}

		if node.Ping() == -1 {
			return nil, 0, errors.ErrPing
		}

		size, err := node.Size()
		if err != nil {
			return nil, 0, err
		}

		if clusterSize > 0 && size != clusterSize {
			return nil, 0, fmt.Errorf("inconsistent size between master and slave")
		}
		clusterSize = size

		hardwareId, err := node.HardwareId()
		if err != nil {
			return nil, 0, err
		}

		nodeId := newNodeId(hardwareId, nodeAddress, clusterSize)
		if _, err := c.clusters.ClusterIdOf(nodeId); err == nil || err != errors.ErrNotFound {
			if err == nil {
				err = errors.ErrRegistered
			}
			return nil, 0, err
		}

		nodeMap[nodeAddress] = &common.Node{
			Id:      nodeId,
			Address: nodeAddress,
			Master:  false,
		}
	}

	r := make(common.NodeList, 0)
	for _, v := range nodeMap {
		r = append(r, v)
	}

	return r, clusterSize, nil
}

func (c *cluster) UnRegisterCluster(clusterId string) error {
	return c.clusters.UnRegisterCluster(clusterId, func(cluster *common.Cluster) error {
		if err := c.index.Replace(clusterId, []string{}); err != nil {
			return err
		}
		for _, node := range cluster.Nodes {
			dn, err := cluster2.NewDataNode(node.Address)
			if err != nil {
				continue
			}
			dn.Wipe()
		}
		return nil
	})
}

func (c *cluster) UnRegisterNode(nodeId string) error {
	return c.clusters.UnRegisterNode(
		nodeId,
		func(clusterId string) error {
			return c.SyncCluster(clusterId)
		},
		func(deletingNode *common.Node) error {
			dn, err := cluster2.NewDataNode(deletingNode.Address)
			if err != nil || !dn.Leave() {
				return errors.ErrMode
			}
			return nil
		},
		func(newMaster *common.Node) error {
			dn, err := cluster2.NewDataNode(newMaster.Address)
			if err != nil || !dn.Mode(true) {
				return errors.ErrMode
			}
			return nil
		})
}

func (c *cluster) Reserve(size uint64) (*common.ReservationMap, error) {
	var reservationMap *common.ReservationMap

	if err := c.clusters.SaveAll(func(clusters common.Clusters) error {
		var err error
		reservationMap, err = c.createReservationMap(size, clusters)

		return err
	}); err != nil {
		return nil, err
	}

	return reservationMap, nil
}

func (c *cluster) Commit(reservationId string, clusterMap map[string]uint64) error {
	return c.clusters.SaveAll(func(clusters common.Clusters) error {
		for _, cluster := range clusters {
			v, has := clusterMap[cluster.Id]
			if !has {
				v = 0
			}
			cluster.Commit(reservationId, v)
		}
		return nil
	})
}

func (c *cluster) Discard(reservationId string) error {
	return c.clusters.SaveAll(func(clusters common.Clusters) error {
		for _, cluster := range clusters {
			cluster.Discard(reservationId)
		}
		return nil
	})
}

func (c *cluster) SyncClusters() error {
	return c.clusters.LockAll(func(clusters common.Clusters) error {
		return c.syncClusters(clusters)
	})
}

func (c *cluster) SyncCluster(clusterId string) error {
	return c.clusters.Lock(clusterId, func(cluster *common.Cluster) error {
		return c.syncClusters(common.Clusters{cluster})
	})
}

func (c *cluster) syncClusters(clusters common.Clusters) error {
	hasError := false

	wg := &sync.WaitGroup{}
	for len(clusters) > 0 {
		cluster := clusters[0]

		masterNode := cluster.Master()
		slaveNodes := cluster.Slaves()

		mdn, err := cluster2.NewDataNode(masterNode.Address)
		if err != nil || !mdn.Join(cluster.Id, masterNode.Id, "") {
			return errors.ErrJoin
		}
		if len(slaveNodes) == 0 {
			clusters = clusters[1:]
			continue
		}

		sha512HexList := mdn.SyncList()
		if sha512HexList == nil {
			fmt.Printf("ERROR: Syncing error: node (%s) didn't response for SyncList\n", masterNode.Id)
			clusters = append(clusters[1:], cluster)
			continue
		}

		if err := c.index.Replace(cluster.Id, sha512HexList); err != nil {
			fmt.Printf("ERROR: Index replacement error: %s\n", err.Error())

			clusters = append(clusters[1:], cluster)
			continue
		}

		for _, slaveNode := range slaveNodes {
			wg.Add(1)
			go func(wg *sync.WaitGroup, mN *common.Node, sN *common.Node) {
				defer wg.Done()

				sdn, err := cluster2.NewDataNode(sN.Address)
				if err != nil || !sdn.Join(cluster.Id, sN.Id, masterNode.Address) {
					fmt.Printf("ERROR: Syncing error: %s\n", errors.ErrJoin.Error())
					hasError = true
					return
				}

				if !sdn.SyncFull(mN.Address) {
					fmt.Printf("ERROR: Syncing node (%s) is failed. Source: %s\n", sN.Id, mN.Address)
					hasError = true
				}
			}(wg, masterNode, slaveNode)
		}

		clusters = clusters[1:]
	}
	wg.Wait()

	if hasError {
		return fmt.Errorf("syncing can not continue because of data node errors")
	}

	return nil
}

func (c *cluster) CheckConsistency() error {
	if err := c.checkStructure(); err != nil {
		return err
	}

	clusters, err := c.GetClusters()
	if err != nil {
		return err
	}

	clusterIds := make([]string, len(clusters))
	for i, cluster := range clusters {
		clusterIds[i] = cluster.Id
	}

	return c.metadata.Cursor(func(folder *common.Folder) (bool, error) {
		changed := false
		for _, file := range folder.Files {
			file.Resurrect()

			missingChunkHashes := make([]string, 0)
			for _, chunk := range file.Chunks {
				if _, err := c.index.Find(clusterIds, chunk.Hash); err != nil {
					if err != errors.ErrNotFound {
						return false, err
					}
					missingChunkHashes = append(missingChunkHashes, chunk.Hash)
				}
			}
			if len(missingChunkHashes) == 0 {
				continue
			}

			file.Ingest([]string{}, missingChunkHashes)
			changed = true
		}
		return changed, nil
	})
}

func (c *cluster) checkStructure() error {
	return c.metadata.LockTree(func(folders []*common.Folder) ([]*common.Folder, error) {
		if len(folders) == 0 {
			return nil, nil
		}

		tree := common.NewTree()
		if err := tree.Fill(folders); err != nil {
			return nil, err
		}

		return tree.Normalize(), nil
	})
}

func (c *cluster) GetClusters() (common.Clusters, error) {
	clusters := make(common.Clusters, 0)
	err := c.clusters.LockAll(func(cs common.Clusters) error {
		for _, c := range cs {
			clusters = append(clusters, c)
		}
		return nil
	})
	return clusters, err
}

func (c *cluster) GetCluster(clusterId string) (*common.Cluster, error) {
	var cluster *common.Cluster
	err := c.clusters.Lock(clusterId, func(c *common.Cluster) error {
		cluster = c
		return nil
	})
	return cluster, err
}

func (c *cluster) Map(sha512HexList []string, mapType common.MapType) (map[string]string, error) {
	clusterMapping := make(map[string]string, 0)
	for _, sha512Hex := range sha512HexList {
		_, address, err := c.Find(sha512Hex, mapType)
		if err != nil {
			if err == errors.ErrNotFound && mapType == common.MT_Delete {
				continue
			}
			return nil, err
		}
		clusterMapping[sha512Hex] = address
	}
	return clusterMapping, nil
}

func (c *cluster) Find(sha512Hex string, mapType common.MapType) (string, string, error) {
	clusterIds := make([]string, 0)
	clusterMap := make(map[string]string)

	if err := c.clusters.LockAll(func(clusters common.Clusters) error {
		for _, cluster := range clusters {
			var node *common.Node

			switch mapType {
			case common.MT_Read:
				node = cluster.HighQualityNode()
			default:
				node = cluster.Master()
			}

			if node == nil {
				return errors.ErrNoAvailableClusterNode
			}

			clusterMap[cluster.Id] = node.Address
			clusterIds = append(clusterIds, cluster.Id)
		}
		return nil
	}); err != nil {
		return "", "", err
	}

	clusterId, err := c.index.Find(clusterIds, sha512Hex)
	if err != nil {
		return "", "", err
	}

	return clusterId, clusterMap[clusterId], nil
}

var _ Cluster = &cluster{}