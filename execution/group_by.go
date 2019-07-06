package execution

import (
	"fmt"

	"github.com/cube2222/octosql"
	"github.com/pkg/errors"
)

type AggregatePrototype func() Aggregate

type Aggregate interface {
	AddRecord(key []interface{}, value interface{}) error
	GetAggregated(key []interface{}) (interface{}, error)
	String() string
}

type GroupBy struct {
	source Node
	key    []Expression

	fields              []octosql.VariableName
	aggregatePrototypes []AggregatePrototype
}

func NewGroupBy(source Node, key []Expression, fields []octosql.VariableName, aggregatePrototypes []AggregatePrototype) *GroupBy {
	return &GroupBy{source: source, key: key, fields: fields, aggregatePrototypes: aggregatePrototypes}
}

func (node *GroupBy) Get(variables octosql.Variables) (RecordStream, error) {
	source, err := node.source.Get(variables)
	if err != nil {
		return nil, errors.Wrap(err, "couldn't get stream for source in group by")
	}

	aggregates := make([]Aggregate, len(node.aggregatePrototypes))
	for i := range node.aggregatePrototypes {
		aggregates[i] = node.aggregatePrototypes[i]()
	}

	return &GroupByStream{
		source:     source,
		variables:  variables,
		key:        node.key,
		fields:     node.fields,
		aggregates: aggregates,
	}, nil
}

type GroupByStream struct {
	source    RecordStream
	variables octosql.Variables

	key    []Expression
	groups *HashMap

	fields     []octosql.VariableName
	aggregates []Aggregate

	fieldNames []octosql.VariableName
	iterator   *Iterator
}

func (stream *GroupByStream) Next() (*Record, error) {
	if stream.iterator == nil {
		for {
			record, err := stream.source.Next()
			if err != nil {
				if err == ErrEndOfStream {
					stream.fieldNames = make([]octosql.VariableName, len(stream.fields))
					for i := range stream.fields {
						stream.fieldNames[i] = octosql.NewVariableName(
							fmt.Sprintf(
								"%s_%s",
								stream.fields[i].String(),
								stream.aggregates[i].String(),
							),
						)
					}
					stream.iterator = stream.groups.GetIterator()
					break
				}
				return nil, errors.Wrap(err, "couldn't get next source record")
			}

			variables, err := stream.variables.MergeWith(record.AsVariables())
			if err != nil {
				return nil, errors.Wrap(err, "couldn't merge stream variables with record")
			}

			key := make([]interface{}, len(stream.key))
			for i := range stream.key {
				key[i], err = stream.key[i].ExpressionValue(variables)
				if err != nil {
					return nil, errors.Wrapf(err, "couldn't evaluate group key expression with index %v", i)
				}
			}

			if len(key) == 0 {
				key = append(key, struct{}{})
			}

			err = stream.groups.Set(key, struct{}{})
			if err != nil {
				return nil, errors.Wrap(err, "couldn't put group key into hashmap")
			}

			for i := range stream.aggregates {
				err := stream.aggregates[i].AddRecord(key, record.Value(stream.fields[i]))
				if err != nil {
					return nil, errors.Wrapf(err, "couldn't add record value to aggregate with index %v", i)
				}
			}
		}
	}

	key, _, ok := stream.iterator.Next()
	if !ok {
		return nil, ErrEndOfStream
	}
	typedKey := key.([]interface{})

	values := make([]interface{}, len(stream.aggregates))
	for i := range stream.aggregates {
		var err error
		values[i], err = stream.aggregates[i].GetAggregated(typedKey)
		if err != nil {
			return nil, errors.Wrap(err, "couldn't get aggregate value")
		}
	}

	return NewRecordFromSlice(stream.fieldNames, values), ErrEndOfStream
}

func (stream *GroupByStream) Close() error {
	return stream.source.Close()
}
