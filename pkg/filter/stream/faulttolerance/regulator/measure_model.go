package regulator

import (
	"fmt"
	v2 "mosn.io/mosn/pkg/config/v2"
	"mosn.io/mosn/pkg/filter/stream/faulttolerance/invocation"
	"mosn.io/mosn/pkg/filter/stream/faulttolerance/util"
	"sync"
	"sync/atomic"
)

type MeasureModel struct {
	key            string
	stats          *sync.Map
	count          int64
	downgradeCount int64
	timeMeter      int64
	config         *v2.FaultToleranceFilterConfig
}

func NewMeasureModel(key string, config *v2.FaultToleranceFilterConfig) *MeasureModel {
	measureModel := &MeasureModel{
		key:            key,
		stats:          new(sync.Map),
		count:          0,
		downgradeCount: 0,
		timeMeter:      0,
		config:         config,
	}
	return measureModel
}

func (m *MeasureModel) GetKey() string {
	return m.key
}

func (m *MeasureModel) AddInvocationStat(stat *invocation.InvocationStat) {
	key := stat.GetInvocationKey()
	if _, ok := m.stats.LoadOrStore(key, stat); !ok {
		atomic.AddInt64(&m.count, 1)
	}
}

func (m *MeasureModel) releaseInvocationStat(stat *invocation.InvocationStat) {
	key := stat.GetInvocationKey()
	m.stats.Delete(key)
	atomic.AddInt64(&m.count, -1)
	invocation.GetInvocationStatFactoryInstance().ReleaseInvocationStat(key)
}

func (m *MeasureModel) Measure() {
	snapshots := m.snapshotInvocations(m.config.RecoverTime)
	ok, averageExceptionRate := m.calculateAverageExceptionRate(snapshots, m.config.LeastWindowCount)
	if !ok {
		return
	}
	for _, snapshot := range snapshots {
		call, _ := snapshot.GetCount()
		if call >= m.config.LeastWindowCount {
			_, exceptionRate := snapshot.GetExceptionRate()
			multiple := util.DivideFloat64(exceptionRate, averageExceptionRate)
			if multiple >= m.config.ExceptionRateMultiple {
				m.downgrade(snapshot, m.config.MaxIpCount)
			}
		}
	}

	m.updateInvocationSnapshots(snapshots)
}

func (m *MeasureModel) downgrade(snapshot *invocation.InvocationStat, maxIpCount int64) {
	if m.count <= 0 {
		return
	}
	if m.downgradeCount+1 > maxIpCount {
		return
	}
	key := snapshot.GetInvocationKey()
	if value, ok := m.stats.Load(key); ok {
		stat := value.(*invocation.InvocationStat)
		stat.Downgrade()
		m.downgradeCount++
	}
}

func (m *MeasureModel) snapshotInvocations(recoverTime int64) []*invocation.InvocationStat {
	snapshots := []*invocation.InvocationStat{}
	m.stats.Range(func(app, value interface{}) bool {
		stat := value.(*invocation.InvocationStat)
		if !stat.IsHealthy() {
			m.recover(stat, recoverTime)
			return true
		}
		if stat.GetCall() <= 0 {
			if stat.AddUselessCycle() {
				m.releaseInvocationStat(stat)
			}
			return true
		} else {
			stat.RestUselessCycle()
		}
		snapshot := value.(*invocation.InvocationStat).Snapshot()
		snapshots = append(snapshots, snapshot)
		return true
	})
	return snapshots
}

func (m *MeasureModel) recover(stat *invocation.InvocationStat, recoverTime int64) {
	if downgradeTime := stat.GetDowngradeTime(); downgradeTime != 0 {
		now := util.GetNowMS()
		if now-downgradeTime >= recoverTime {
			stat.Recover()
			m.downgradeCount--
		}
	}
}

func (m *MeasureModel) updateInvocationSnapshots(snapshots []*invocation.InvocationStat) {
	for _, snapshot := range snapshots {
		if value, ok := m.stats.Load(snapshot.GetInvocationKey()); ok {
			stat := value.(*invocation.InvocationStat)
			stat.Update(snapshot)
		}
	}
}

func (m *MeasureModel) calculateAverageExceptionRate(stats []*invocation.InvocationStat, leastWindowCount int64) (bool, float64) {
	var sumException int64
	var sumCall int64
	for _, stat := range stats {
		if call, exception := stat.GetCount(); call >= leastWindowCount {
			sumException += exception
			sumCall += call
		}
	}
	if sumCall == 0 {
		return false, 0
	}
	return true, util.DivideInt64(sumException, sumCall)
}

func (m *MeasureModel) IsArrivalTime() bool {
	timeWindow := m.config.TimeWindow
	now := util.GetNowMS()

	if m.timeMeter == 0 {
		m.timeMeter = now + timeWindow
		return false
	} else {
		if now >= m.timeMeter {
			m.timeMeter = now + timeWindow
			return true
		} else {
			return false
		}
	}
}

func (m *MeasureModel) String() string {
	str := fmt.Sprintf("key=%s,count=%v,downgradeCount=%v,timeMeter=%v",
		m.key, m.count, m.downgradeCount, m.timeMeter)
	return str
}
