package model

import (
	"github.com/coroot/coroot/timeseries"
)

type KeyspacesTableKey struct {
	Keyspace string
	Table    string
}

func (k KeyspacesTableKey) String() string {
	return k.Keyspace + "." + k.Table
}

type KeyspacesTable struct {
	Keyspace     string
	Table        string
	CapacityMode LabelLastValue // PROVISIONED | PAY_PER_REQUEST
	Status       LabelLastValue // ACTIVE | ...

	ProvisionedRead  *timeseries.TimeSeries
	ProvisionedWrite *timeseries.TimeSeries
	WarmRead         *timeseries.TimeSeries
	WarmWrite        *timeseries.TimeSeries

	ConsumedRead   *timeseries.TimeSeries
	ConsumedWrite  *timeseries.TimeSeries
	TableSizeBytes *timeseries.TimeSeries

	RequestsWindow map[string]*timeseries.TimeSeries // by operation
	LatencyP99     map[string]*timeseries.TimeSeries // by operation (seconds)

	ReadThrottleEvents   *timeseries.TimeSeries
	WriteThrottleEvents  *timeseries.TimeSeries
	PartitionLimitEvents *timeseries.TimeSeries
	UserErrors           *timeseries.TimeSeries
	SystemErrors         *timeseries.TimeSeries
}

func NewKeyspacesTable(keyspace, table string) *KeyspacesTable {
	return &KeyspacesTable{
		Keyspace:       keyspace,
		Table:          table,
		RequestsWindow: map[string]*timeseries.TimeSeries{},
		LatencyP99:     map[string]*timeseries.TimeSeries{},
	}
}

func (t *KeyspacesTable) IsProvisioned() bool {
	return t.CapacityMode.Value() == "PROVISIONED"
}

func (t *KeyspacesTable) IsOnDemand() bool {
	return t.CapacityMode.Value() == "PAY_PER_REQUEST"
}

type Keyspaces struct {
	Id      string // <account>/<region>
	Account LabelLastValue
	Region  LabelLastValue
	Status  LabelLastValue

	LifeSpan  *timeseries.TimeSeries
	Endpoints map[string]string // addr -> kind (public|vpc|peer)
	Tables    map[KeyspacesTableKey]*KeyspacesTable

	AccountUserErrors        map[string]*timeseries.TimeSeries // operation: EXECUTE | PREPARE
	PerConnectionLimitEvents *timeseries.TimeSeries
}

func NewKeyspaces(id string) *Keyspaces {
	return &Keyspaces{
		Id:                id,
		Endpoints:         map[string]string{},
		Tables:            map[KeyspacesTableKey]*KeyspacesTable{},
		AccountUserErrors: map[string]*timeseries.TimeSeries{},
	}
}

func (k *Keyspaces) GetOrCreateTable(keyspace, table string) *KeyspacesTable {
	tk := KeyspacesTableKey{Keyspace: keyspace, Table: table}
	t := k.Tables[tk]
	if t == nil {
		t = NewKeyspacesTable(keyspace, table)
		k.Tables[tk] = t
	}
	return t
}

func (k *Keyspaces) ApplicationType() ApplicationType {
	if k == nil {
		return ApplicationTypeUnknown
	}
	return ApplicationTypeCassandra
}

func (k *Keyspaces) IsUp() bool {
	if k == nil {
		return false
	}
	return !k.LifeSpan.TailIsEmpty()
}

func (k *Keyspaces) HasCloudWatchData() bool {
	if k == nil {
		return false
	}
	if !k.PerConnectionLimitEvents.IsEmpty() {
		return true
	}
	for _, ts := range k.AccountUserErrors {
		if !ts.IsEmpty() {
			return true
		}
	}
	for _, t := range k.Tables {
		if !t.ConsumedRead.IsEmpty() || !t.ConsumedWrite.IsEmpty() || !t.TableSizeBytes.IsEmpty() ||
			!t.ReadThrottleEvents.IsEmpty() || !t.WriteThrottleEvents.IsEmpty() ||
			!t.PartitionLimitEvents.IsEmpty() || !t.UserErrors.IsEmpty() || !t.SystemErrors.IsEmpty() {
			return true
		}
		for _, ts := range t.RequestsWindow {
			if !ts.IsEmpty() {
				return true
			}
		}
		for _, ts := range t.LatencyP99 {
			if !ts.IsEmpty() {
				return true
			}
		}
	}
	return false
}
