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

package plan

import (
	"github.com/juju/errors"
	"github.com/pingcap/tidb/ast"
	"github.com/pingcap/tidb/expression"
	"github.com/pingcap/tidb/model"
	"github.com/pingcap/tidb/statistics"
	"github.com/pingcap/tidb/util/types"
)

// JoinType contains CrossJoin, InnerJoin, LeftOuterJoin, RightOuterJoin, FullOuterJoin, SemiJoin.
type JoinType int

const (
	// InnerJoin means inner join.
	InnerJoin JoinType = iota
	// LeftOuterJoin means left join.
	LeftOuterJoin
	// RightOuterJoin means right join.
	RightOuterJoin
	// SemiJoin means if row a in table A matches some rows in B, just output a.
	SemiJoin
	// LeftOuterSemiJoin means if row a in table A matches some rows in B, output (a, true), otherwise, output (a, false).
	LeftOuterSemiJoin
)

// Join is the logical join plan.
type Join struct {
	baseLogicalPlan

	JoinType        JoinType
	anti            bool
	reordered       bool
	cartesianJoin   bool
	preferMergeJoin bool

	EqualConditions []*expression.ScalarFunction
	LeftConditions  []expression.Expression
	RightConditions []expression.Expression
	OtherConditions []expression.Expression

	// DefaultValues is only used for outer join, which stands for the default values when the outer table cannot find join partner
	// instead of null padding.
	DefaultValues []types.Datum
}

func (p *Join) columnSubstitute(schema *expression.Schema, exprs []expression.Expression) {
	for i, fun := range p.EqualConditions {
		p.EqualConditions[i] = expression.ColumnSubstitute(fun, schema, exprs).(*expression.ScalarFunction)
	}
	for i, fun := range p.LeftConditions {
		p.LeftConditions[i] = expression.ColumnSubstitute(fun, schema, exprs)
	}
	for i, fun := range p.RightConditions {
		p.RightConditions[i] = expression.ColumnSubstitute(fun, schema, exprs)
	}
	for i, fun := range p.OtherConditions {
		p.OtherConditions[i] = expression.ColumnSubstitute(fun, schema, exprs)
	}
}

func (p *Join) attachOnConds(onConds []expression.Expression) {
	eq, left, right, other := extractOnCondition(onConds, p.children[0].(LogicalPlan), p.children[1].(LogicalPlan))
	p.EqualConditions = append(eq, p.EqualConditions...)
	p.LeftConditions = append(left, p.LeftConditions...)
	p.RightConditions = append(right, p.RightConditions...)
	p.OtherConditions = append(other, p.OtherConditions...)
}

func (p *Join) extractCorrelatedCols() []*expression.CorrelatedColumn {
	corCols := p.basePlan.extractCorrelatedCols()
	for _, fun := range p.EqualConditions {
		corCols = append(corCols, extractCorColumns(fun)...)
	}
	for _, fun := range p.LeftConditions {
		corCols = append(corCols, extractCorColumns(fun)...)
	}
	for _, fun := range p.RightConditions {
		corCols = append(corCols, extractCorColumns(fun)...)
	}
	for _, fun := range p.OtherConditions {
		corCols = append(corCols, extractCorColumns(fun)...)
	}
	return corCols
}

// Projection represents a select fields plan.
type Projection struct {
	baseLogicalPlan
	Exprs []expression.Expression
}

func (p *Projection) extractCorrelatedCols() []*expression.CorrelatedColumn {
	corCols := p.basePlan.extractCorrelatedCols()
	for _, expr := range p.Exprs {
		corCols = append(corCols, extractCorColumns(expr)...)
	}
	return corCols
}

// Aggregation represents an aggregate plan.
type Aggregation struct {
	baseLogicalPlan

	AggFuncs     []expression.AggregationFunction
	GroupByItems []expression.Expression

	// groupByCols stores the columns that are group-by items.
	groupByCols []*expression.Column
}

func (p *Aggregation) extractCorrelatedCols() []*expression.CorrelatedColumn {
	corCols := p.basePlan.extractCorrelatedCols()
	for _, expr := range p.GroupByItems {
		corCols = append(corCols, extractCorColumns(expr)...)
	}
	for _, fun := range p.AggFuncs {
		for _, arg := range fun.GetArgs() {
			corCols = append(corCols, extractCorColumns(arg)...)
		}
	}
	return corCols
}

// Selection means a filter.
type Selection struct {
	baseLogicalPlan

	// Originally the WHERE or ON condition is parsed into a single expression,
	// but after we converted to CNF(Conjunctive normal form), it can be
	// split into a list of AND conditions.
	Conditions []expression.Expression

	// onTable means if this selection's child is a table scan or index scan.
	onTable bool

	// If ScanController is true, then the child of this selection is a scan,
	// which use pk or index. we will record the accessConditions, idxConditions,
	// and tblConditions to control the below plan.
	ScanController bool

	// We will check this at decorrelate phase.
	canControlScan bool
}

func (p *Selection) extractCorrelatedCols() []*expression.CorrelatedColumn {
	corCols := p.basePlan.extractCorrelatedCols()
	for _, cond := range p.Conditions {
		corCols = append(corCols, extractCorColumns(cond)...)
	}
	return corCols
}

// Apply gets one row from outer executor and gets one row from inner executor according to outer row.
type Apply struct {
	Join

	corCols []*expression.CorrelatedColumn
}

func (p *Apply) extractCorrelatedCols() []*expression.CorrelatedColumn {
	corCols := p.Join.extractCorrelatedCols()
	for i := len(corCols) - 1; i >= 0; i-- {
		if p.children[0].Schema().Contains(&corCols[i].Column) {
			corCols = append(corCols[:i], corCols[i+1:]...)
		}
	}
	return corCols
}

// Exists checks if a query returns result.
type Exists struct {
	baseLogicalPlan
}

// MaxOneRow checks if a query returns no more than one row.
type MaxOneRow struct {
	baseLogicalPlan
}

// TableDual represents a dual table plan.
type TableDual struct {
	baseLogicalPlan
}

// DataSource represents a tablescan without condition push down.
type DataSource struct {
	baseLogicalPlan

	indexHints []*ast.IndexHint
	tableInfo  *model.TableInfo
	Columns    []*model.ColumnInfo
	DBName     model.CIStr

	TableAsName *model.CIStr

	LimitCount *int64

	statisticTable *statistics.Table
}

// Union represents Union plan.
type Union struct {
	baseLogicalPlan
}

// Sort stands for the order by plan.
type Sort struct {
	baseLogicalPlan

	ByItems   []*ByItems
	ExecLimit *Limit
}

func (p *Sort) extractCorrelatedCols() []*expression.CorrelatedColumn {
	corCols := p.basePlan.extractCorrelatedCols()
	for _, item := range p.ByItems {
		corCols = append(corCols, extractCorColumns(item.Expr)...)
	}
	return corCols
}

// Update represents Update plan.
type Update struct {
	baseLogicalPlan

	OrderedList []*expression.Assignment
}

// Delete represents a delete plan.
type Delete struct {
	baseLogicalPlan

	Tables       []*ast.TableName
	IsMultiTable bool
}

// AddChild for parent.
func addChild(parent Plan, child Plan) {
	if child == nil || parent == nil {
		return
	}
	child.AddParent(parent)
	parent.AddChild(child)
}

// InsertPlan means inserting plan between two plans.
func InsertPlan(parent Plan, child Plan, insert Plan) error {
	err := child.ReplaceParent(parent, insert)
	if err != nil {
		return errors.Trace(err)
	}
	err = parent.ReplaceChild(child, insert)
	if err != nil {
		return errors.Trace(err)
	}
	insert.AddChild(child)
	insert.AddParent(parent)
	return nil
}

// RemovePlan means removing a plan.
func RemovePlan(p Plan) error {
	parents := p.Parents()
	children := p.Children()
	if len(parents) > 1 || len(children) != 1 {
		return SystemInternalErrorType.Gen("can't remove this plan")
	}
	if len(parents) == 0 {
		child := children[0]
		child.SetParents()
		return nil
	}
	parent, child := parents[0], children[0]
	err := parent.ReplaceChild(p, child)
	if err != nil {
		return errors.Trace(err)
	}
	err = child.ReplaceParent(p, parent)
	return errors.Trace(err)
}