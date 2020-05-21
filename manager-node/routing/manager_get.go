package routing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/freakmaxi/kertish-dfs/basics/common"
	"github.com/freakmaxi/kertish-dfs/basics/errors"
)

func (m *managerRouter) handleGet(w http.ResponseWriter, r *http.Request) {
	action := r.Header.Get("X-Action")

	if !m.validateGetAction(action) {
		w.WriteHeader(422)
		return
	}

	switch action {
	case "sync":
		m.handleSync(w, r)
	case "check":
		m.handleCheckConsistency(w, r)
	case "move":
		m.handleMove(w, r)
	case "balance":
		m.handleBalance(w, r)
	case "clusters":
		m.handleClusters(w, r)
	case "find":
		m.handleFind(w, r)
	default:
		w.WriteHeader(406)
	}
}

func (m *managerRouter) handleSync(w http.ResponseWriter, r *http.Request) {
	clusterId := r.Header.Get("X-Options")

	var err error
	if len(clusterId) == 0 {
		if errorList := m.manager.SyncClusters(); len(errorList) > 0 {
			err = errors.ErrSync
		}
	} else {
		err = m.manager.SyncCluster(clusterId)
	}

	if err == nil {
		return
	}

	if err == errors.ErrNotFound {
		w.WriteHeader(404)
	} else {
		w.WriteHeader(500)
	}

	e := common.NewError(100, err.Error())
	if err := json.NewEncoder(w).Encode(e); err != nil {
		fmt.Printf("ERROR: Get request is failed. %s\n", err.Error())
	}
}

func (m *managerRouter) handleCheckConsistency(w http.ResponseWriter, r *http.Request) {
	err := m.manager.CheckConsistency()
	if err == nil {
		return
	}

	if err == errors.ErrNotFound {
		w.WriteHeader(404)
	} else {
		w.WriteHeader(500)
	}

	e := common.NewError(105, err.Error())
	if err := json.NewEncoder(w).Encode(e); err != nil {
		fmt.Printf("ERROR: Get request is failed. %s\n", err.Error())
	}
}

func (m *managerRouter) handleMove(w http.ResponseWriter, r *http.Request) {
	sourceClusterId, targetClusterId, valid := m.describeMoveOptions(r.Header.Get("X-Options"))
	if !valid {
		w.WriteHeader(422)
		return
	}

	if err := m.manager.MoveCluster(sourceClusterId, targetClusterId); err != nil {
		if err == errors.ErrNotFound {
			w.WriteHeader(404)
		} else if err == errors.ErrNotAvailableForClusterAction {
			w.WriteHeader(503)
		} else if err == errors.ErrNoSpace {
			w.WriteHeader(507)
		} else {
			w.WriteHeader(500)
		}

		e := common.NewError(130, err.Error())
		if err := json.NewEncoder(w).Encode(e); err != nil {
			fmt.Printf("ERROR: Get request is failed. %s\n", err.Error())
		}
	}
}

func (m *managerRouter) handleBalance(w http.ResponseWriter, r *http.Request) {
	clusterIds, valid := m.describeBalanceOptions(r.Header.Get("X-Options"))
	if !valid {
		w.WriteHeader(422)
		return
	}

	if err := m.manager.BalanceClusters(clusterIds); err != nil {
		if err == errors.ErrNotFound {
			w.WriteHeader(404)
		} else if err == errors.ErrNotAvailableForClusterAction {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(500)
		}

		e := common.NewError(135, err.Error())
		if err := json.NewEncoder(w).Encode(e); err != nil {
			fmt.Printf("ERROR: Get request is failed. %s\n", err.Error())
		}
	}
}

func (m *managerRouter) handleClusters(w http.ResponseWriter, r *http.Request) {
	clusterId := r.Header.Get("X-Options")

	var clusters common.Clusters
	var err error
	if len(clusterId) == 0 {
		clusters, err = m.manager.GetClusters()
	} else {
		cluster, e := m.manager.GetCluster(clusterId)
		if e == nil {
			clusters = common.Clusters{cluster}
		}
		err = e
	}

	if err == nil {
		if err := json.NewEncoder(w).Encode(clusters); err != nil {
			fmt.Printf("ERROR: Get request is failed. %s\n", err.Error())
		}
		return
	}

	if err == errors.ErrNotFound {
		w.WriteHeader(404)
	} else {
		w.WriteHeader(500)
	}

	e := common.NewError(110, err.Error())
	if err := json.NewEncoder(w).Encode(e); err != nil {
		fmt.Printf("ERROR: Get request is failed. %s\n", err.Error())
	}
}

func (m *managerRouter) handleFind(w http.ResponseWriter, r *http.Request) {
	sha512Hex := r.Header.Get("X-Options")

	clusterId, address, err := m.manager.Find(sha512Hex, common.MT_Create)

	if err == nil {
		w.Header().Set("X-Cluster-Id", clusterId)
		w.Header().Set("X-Address", address)
		return
	}

	if err == errors.ErrNotFound {
		w.WriteHeader(404)
	} else if err == errors.ErrNoAvailableClusterNode {
		w.WriteHeader(503)
	} else {
		w.WriteHeader(500)
	}

	e := common.NewError(120, err.Error())
	if err := json.NewEncoder(w).Encode(e); err != nil {
		fmt.Printf("ERROR: Get request is failed. %s\n", err.Error())
	}
}

func (m *managerRouter) validateGetAction(action string) bool {
	switch action {
	case "sync", "check", "move", "balance", "clusters", "find":
		return true
	}
	return false
}

func (m *managerRouter) describeMoveOptions(options string) (string, string, bool) {
	sourceClusterId := ""
	targetClusterId := ""

	commaIdx := strings.Index(options, ",")
	if commaIdx > -1 {
		sourceClusterId = options[:commaIdx]
		targetClusterId = options[commaIdx+1:]
	}

	return sourceClusterId, targetClusterId, len(sourceClusterId) > 0 && len(targetClusterId) > 0
}

func (m *managerRouter) describeBalanceOptions(options string) ([]string, bool) {
	clusterIds := make([]string, 0)

	for len(options) > 0 {
		commaIdx := strings.Index(options, ",")
		if commaIdx == -1 {
			if len(options) > 0 {
				clusterIds = append(clusterIds, options)
			}
			break
		}

		clusterId := options[:commaIdx]
		if len(clusterId) > 0 {
			clusterIds = append(clusterIds, clusterId)
		}
		options = options[commaIdx+1:]
	}

	if len(clusterIds) == 1 {
		return nil, false
	}
	return clusterIds, true
}
