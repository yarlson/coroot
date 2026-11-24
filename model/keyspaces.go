package model

import (
	"github.com/coroot/coroot/timeseries"
)

const (
	KeyspacesCapacityModeProvisioned = "provisioned"
	KeyspacesCapacityRead            = "read"
	KeyspacesCapacityWrite           = "write"

	KeyspacesErrorHotPartition       = "hot_partition"
	KeyspacesErrorReadThrottle       = "read_throttle"
	KeyspacesErrorWriteThrottle      = "write_throttle"
	KeyspacesErrorUser               = "user"
	KeyspacesErrorSystem             = "system"
	KeyspacesErrorPerConnectionLimit = "per_connection_limit"

	KeyspacesOperationExecute = "EXECUTE"
	KeyspacesOperationPrepare = "PREPARE"

	KeyspacesErrorsWindow = 5 * timeseries.Minute
)

type Keyspaces struct {
	Id        string // the keyspaces_id label: <account>/<region>
	AccountId string
	Region    string

	LifeSpan *timeseries.TimeSeries

	Tables map[KeyspacesTableKey]*KeyspacesTable

	AccountErrors map[KeyspacesAccountErrorKey]*timeseries.TimeSeries // rejections in the trailing 5-minute window
}

type KeyspacesTableKey struct {
	Keyspace string
	Table    string
}

func (k KeyspacesTableKey) String() string {
	return k.Keyspace + "." + k.Table
}

type KeyspacesAccountErrorKey struct {
	Type      string
	Operation string
}

type KeyspacesTable struct {
	CapacityMode LabelLastValue
	AutoScaling  LabelLastValue

	ProvisionedRead  *timeseries.TimeSeries
	ProvisionedWrite *timeseries.TimeSeries
	ConsumedRead     *timeseries.TimeSeries // capacity units per second
	ConsumedWrite    *timeseries.TimeSeries // capacity units per second
	SizeBytes        *timeseries.TimeSeries

	Latency  map[string]*timeseries.TimeSeries // p99 in seconds, by operation
	Requests map[string]*timeseries.TimeSeries // successful requests in the trailing 5-minute window, by operation
	Errors   map[string]*timeseries.TimeSeries // rejections in the trailing 5-minute window, by type
}

func NewKeyspaces(id, accountId, region string) *Keyspaces {
	return &Keyspaces{
		Id:            id,
		AccountId:     accountId,
		Region:        region,
		Tables:        map[KeyspacesTableKey]*KeyspacesTable{},
		AccountErrors: map[KeyspacesAccountErrorKey]*timeseries.TimeSeries{},
	}
}

func (k *Keyspaces) GetOrCreateTable(key KeyspacesTableKey) *KeyspacesTable {
	t := k.Tables[key]
	if t == nil {
		t = &KeyspacesTable{
			Latency:  map[string]*timeseries.TimeSeries{},
			Requests: map[string]*timeseries.TimeSeries{},
			Errors:   map[string]*timeseries.TimeSeries{},
		}
		k.Tables[key] = t
	}
	return t
}

func (k *Keyspaces) IsUp() bool {
	return k != nil && !timeseries.IsNaN(k.LifeSpan.Last())
}

func (k *Keyspaces) ApplicationType() ApplicationType {
	if k == nil {
		return ApplicationTypeUnknown
	}
	return ApplicationTypeCassandra
}

func (t *KeyspacesTable) IsProvisioned() bool {
	return t.CapacityMode.Value() == KeyspacesCapacityModeProvisioned
}

func (t *KeyspacesTable) HasData() bool {
	return !t.ConsumedRead.IsEmpty() || !t.ConsumedWrite.IsEmpty() || !t.SizeBytes.IsEmpty() || len(t.Requests) > 0 || len(t.Errors) > 0
}
