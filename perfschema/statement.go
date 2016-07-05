// Copyright 2016 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package perfschema

import (
	"fmt"
	"reflect"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/juju/errors"
	"github.com/ngaut/log"
	"github.com/pingcap/tidb/ast"
	"github.com/pingcap/tidb/util/types"
)

// statementInfo defines statement instrument information.
type statementInfo struct {
	// The registered statement key
	key uint64
	// The name of the statement instrument to register
	name string
}

// StatementState provides temporary storage to a statement runtime statistics.
// TODO:
// 1. support statement digest.
// 2. support prepared statement.
type StatementState struct {
	// Connection identifier
	connID uint64
	// Statement information
	info *statementInfo
	// Statement type
	stmtType reflect.Type
	// Source file and line number
	source string
	// Timer name
	timerName enumTimerName
	// Timer start
	timerStart int64
	// Timer end
	timerEnd int64
	// Locked time
	lockTime int64
	// SQL statement string
	sqlText string
	// Current schema name
	schemaName string
	// Number of errors
	errNum uint32
	// Number of warnings
	warnNum uint32
	// Rows affected
	rowsAffected uint64
	// Rows sent
	rowsSent uint64
	// Rows examined
	rowsExamined uint64
	// Metric, temporary tables created on disk
	createdTmpDiskTables uint32
	// Metric, temproray tables created
	createdTmpTables uint32
	// Metric, number of select full join
	selectFullJoin uint32
	// Metric, number of select full range join
	selectFullRangeJoin uint32
	// Metric, number of select range
	selectRange uint32
	// Metric, number of select range check
	selectRangeCheck uint32
	// Metric, number of select scan
	selectScan uint32
	// Metric, number of sort merge passes
	sortMergePasses uint32
	// Metric, number of sort merge
	sortRange uint32
	// Metric, number of sort rows
	sortRows uint32
	// Metric, number of sort scans
	sortScan uint32
	// Metric, no index used flag
	noIndexUsed uint8
	// Metric, no good index used flag
	noGoodIndexUsed uint8
}

func (ps *perfSchema) RegisterStatement(category, name string, elem interface{}) {
	instrumentName := fmt.Sprintf("%s%s/%s", statementInstrumentPrefix, category, name)
	key, err := ps.addInstrument(instrumentName)
	if err != nil {
		// just ignore, do nothing else.
		log.Errorf("Unable to register instrument %s", instrumentName)
		return
	}

	ps.stmtInfos[reflect.TypeOf(elem)] = &statementInfo{
		key:  key,
		name: instrumentName,
	}
}

func (ps *perfSchema) StartStatement(sql string, connID uint64, callerName EnumCallerName, elem interface{}) *StatementState {
	stmtType := reflect.TypeOf(elem)
	info, ok := ps.stmtInfos[stmtType]
	if !ok {
		// just ignore, do nothing else.
		log.Errorf("No instrument registered for statement %s", stmtType)
		return nil
	}

	// check and apply the configuration parameter in table setup_timers.
	timerName, err := ps.getTimerName(flagStatement)
	if err != nil {
		// just ignore, do nothing else.
		log.Error("Unable to check setup_timers table")
		return nil
	}
	var timerStart int64
	switch timerName {
	case timerNameNanosec:
		timerStart = time.Now().UnixNano()
	case timerNameMicrosec:
		timerStart = time.Now().UnixNano() / int64(time.Microsecond)
	case timerNameMillisec:
		timerStart = time.Now().UnixNano() / int64(time.Millisecond)
	default:
		return nil
	}

	// TODO: check and apply the additional configuration parameters in:
	// - table setup_actors
	// - table setup_setup_consumers
	// - table setup_instruments
	// - table setup_objects

	var source string
	callerLock.RLock()
	source, ok = callerNames[callerName]
	callerLock.RUnlock()
	if !ok {
		_, fileName, fileLine, ok := runtime.Caller(1)
		if !ok {
			// just ignore, do nothing else.
			log.Error("Unable to get runtime.Caller(1)")
			return nil
		}
		source = fmt.Sprintf("%s:%d", fileName, fileLine)

		callerLock.Lock()
		callerNames[callerName] = source
		callerLock.Unlock()
	}

	return &StatementState{
		connID:     connID,
		info:       info,
		stmtType:   stmtType,
		source:     source,
		timerName:  timerName,
		timerStart: timerStart,
		sqlText:    sql,
	}
}

func (ps *perfSchema) EndStatement(state *StatementState) {
	if state == nil {
		return
	}

	switch state.timerName {
	case timerNameNanosec:
		state.timerEnd = time.Now().UnixNano()
	case timerNameMicrosec:
		state.timerEnd = time.Now().UnixNano() / int64(time.Microsecond)
	case timerNameMillisec:
		state.timerEnd = time.Now().UnixNano() / int64(time.Millisecond)
	default:
		return
	}

	log.Debugf("EndStatement: sql %s, connection id %d, type %s", state.sqlText, state.connID, state.stmtType)

	record := state2Record(state)
	err := ps.updateEventsStmtsCurrent(state.connID, record)
	if err != nil {
		log.Error("Unable to update events_statements_current table")
	}
	err = ps.appendEventsStmtsHistory(record)
	if err != nil {
		log.Errorf("Unable to append to events_statements_history table %v", errors.ErrorStack(err))
	}
}

func state2Record(state *StatementState) []types.Datum {
	ret := make([]types.Datum, 41)
	ret[0].SetUint64(state.connID)                        // THREAD_ID
	ret[1].SetUint64(state.info.key)                      // EVENT_ID
	ret[2].SetNull()                                      // END_EVENT_ID
	ret[3].SetString(state.info.name)                     // EVENT_NAME
	ret[4].SetString(state.source)                        // SOURCE
	ret[5].SetUint64(uint64(state.timerStart))            // TIMER_START
	ret[6].SetUint64(uint64(state.timerEnd))              // TIMER_END
	ret[7].SetNull()                                      // TIMER_WAIT
	ret[8].SetUint64(uint64(state.lockTime))              // LOCK_TIME
	ret[9].SetString(state.sqlText)                       // SQL_TEXT
	ret[10].SetNull()                                     // DIGEST
	ret[11].SetNull()                                     // DIGEST_TEXT
	ret[12].SetString(state.schemaName)                   // CURRENT_SCHEMA
	ret[13].SetNull()                                     // OBJECT_TYPE
	ret[14].SetNull()                                     // OBJECT_SCHEMA
	ret[15].SetNull()                                     // OBJECT_NAME
	ret[16].SetNull()                                     // OBJECT_INSTANCE_BEGIN
	ret[17].SetNull()                                     // MYSQL_ERRNO,
	ret[18].SetNull()                                     // RETURNED_SQLSTATE
	ret[19].SetNull()                                     // MESSAGE_TEXT
	ret[20].SetUint64(uint64(state.errNum))               // ERRORS
	ret[21].SetUint64(uint64(state.warnNum))              // WARNINGS
	ret[22].SetUint64(state.rowsAffected)                 // ROWS_AFFECTED
	ret[23].SetUint64(state.rowsSent)                     // ROWS_SENT
	ret[24].SetUint64(state.rowsExamined)                 // ROWS_EXAMINED
	ret[25].SetUint64(uint64(state.createdTmpDiskTables)) // CREATED_TMP_DISK_TABLES
	ret[26].SetUint64(uint64(state.createdTmpTables))     // CREATED_TMP_TABLES
	ret[27].SetUint64(uint64(state.selectFullJoin))       // SELECT_FULL_JOIN
	ret[28].SetUint64(uint64(state.selectFullRangeJoin))  // SELECT_FULL_RANGE_JOIN
	ret[29].SetUint64(uint64(state.selectRange))          // SELECT_RANGE
	ret[30].SetUint64(uint64(state.selectRangeCheck))     // SELECT_RANGE_CHECK
	ret[31].SetUint64(uint64(state.selectScan))           // SELECT_SCAN
	ret[32].SetUint64(uint64(state.sortMergePasses))      // SORT_MERGE_PASSES
	ret[33].SetUint64(uint64(state.sortRange))            // SORT_RANGE
	ret[34].SetUint64(uint64(state.sortRows))             // SORT_ROWS
	ret[35].SetUint64(uint64(state.sortScan))             // SORT_SCAN
	ret[36].SetUint64(uint64(state.noIndexUsed))          // NO_INDEX_USED
	ret[37].SetUint64(uint64(state.noGoodIndexUsed))      // NO_GOOD_INDEX_USED
	ret[38].SetNull()                                     // NESTING_EVENT_ID
	ret[39].SetNull()                                     // NESTING_EVENT_TYPE
	ret[40].SetNull()                                     // NESTING_EVENT_LEVEL
	return ret
}

func (ps *perfSchema) updateEventsStmtsCurrent(connID uint64, record []types.Datum) error {
	tbl := ps.mTables[TableStmtsCurrent]
	if tbl == nil {
		return nil
	}
	index := connID % uint64(currentElemMax)
	handle := atomic.LoadInt64(&ps.stmtHandles[index])
	if handle == 0 {
		newHandle, err := tbl.AddRecord(nil, record)
		if err != nil {
			return errors.Trace(err)
		}
		atomic.StoreInt64(&ps.stmtHandles[index], newHandle)
		return nil
	}
	err := tbl.UpdateRecord(nil, handle, nil, record, nil)
	if err != nil {
		return errors.Trace(err)
	}
	return nil
}

func (ps *perfSchema) appendEventsStmtsHistory(record []types.Datum) error {
	tbl := ps.mTables[TableStmtsHistory]
	if tbl == nil {
		return nil
	}
	_, err := tbl.AddRecord(nil, record)
	if err != nil {
		return errors.Trace(err)
	}
	return nil
}

func (ps *perfSchema) registerStatements() {
	ps.stmtInfos = make(map[reflect.Type]*statementInfo)
	// Existing instrument names are the same as MySQL 5.7
	ps.RegisterStatement("sql", "alter_table", (*ast.AlterTableStmt)(nil))
	ps.RegisterStatement("sql", "begin", (*ast.BeginStmt)(nil))
	ps.RegisterStatement("sql", "commit", (*ast.CommitStmt)(nil))
	ps.RegisterStatement("sql", "create_db", (*ast.CreateDatabaseStmt)(nil))
	ps.RegisterStatement("sql", "create_index", (*ast.CreateIndexStmt)(nil))
	ps.RegisterStatement("sql", "create_table", (*ast.CreateTableStmt)(nil))
	ps.RegisterStatement("sql", "create_user", (*ast.CreateUserStmt)(nil))
	ps.RegisterStatement("sql", "deallocate", (*ast.DeallocateStmt)(nil))
	ps.RegisterStatement("sql", "delete", (*ast.DeleteStmt)(nil))
	ps.RegisterStatement("sql", "do", (*ast.DoStmt)(nil))
	ps.RegisterStatement("sql", "drop_db", (*ast.DropDatabaseStmt)(nil))
	ps.RegisterStatement("sql", "drop_table", (*ast.DropTableStmt)(nil))
	ps.RegisterStatement("sql", "drop_index", (*ast.DropIndexStmt)(nil))
	ps.RegisterStatement("sql", "execute", (*ast.ExecuteStmt)(nil))
	ps.RegisterStatement("sql", "explain", (*ast.ExplainStmt)(nil))
	ps.RegisterStatement("sql", "grant", (*ast.GrantStmt)(nil))
	ps.RegisterStatement("sql", "insert", (*ast.InsertStmt)(nil))
	ps.RegisterStatement("sql", "prepare", (*ast.PrepareStmt)(nil))
	ps.RegisterStatement("sql", "rollback", (*ast.RollbackStmt)(nil))
	ps.RegisterStatement("sql", "select", (*ast.SelectStmt)(nil))
	ps.RegisterStatement("sql", "set", (*ast.SetStmt)(nil))
	ps.RegisterStatement("sql", "set_charset", (*ast.SetCharsetStmt)(nil))
	ps.RegisterStatement("sql", "set_password", (*ast.SetPwdStmt)(nil))
	ps.RegisterStatement("sql", "show", (*ast.ShowStmt)(nil))
	ps.RegisterStatement("sql", "truncate", (*ast.TruncateTableStmt)(nil))
	ps.RegisterStatement("sql", "union", (*ast.UnionStmt)(nil))
	ps.RegisterStatement("sql", "update", (*ast.UpdateStmt)(nil))
	ps.RegisterStatement("sql", "use", (*ast.UseStmt)(nil))
	ps.RegisterStatement("sql", "analyze", (*ast.AnalyzeTableStmt)(nil))
}
