// Copyright 2014 The Cayley Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package elastic

import (
	"context"
	"fmt"
	"strings"

	"github.com/cayleygraph/cayley/graph"
	"github.com/cayleygraph/cayley/graph/iterator"
	"github.com/cayleygraph/cayley/quad"
	elastic "gopkg.in/olivere/elastic.v5"
)

// Iterator struct used for elastic backend
type Iterator struct {
	uid         uint64
	tags        graph.Tagger
	qs          *QuadStore
	dir         quad.Direction
	resultSet   *elastic.SearchResult
	resultIndex int64
	scrollId    string
	hash        graph.Value
	size        int64
	isAll       bool
	resultType  string
	types       string
	query       elastic.Query
	result      graph.Value
	err         error
}

// SearchResultsIterator contains the results of the search
type SearchResultsIterator struct {
	index         uint64
	searchResults *elastic.SearchResult
}

var ctx = context.Background()

func getFieldMap(qs *QuadStore) map[string]interface{} {
	fields, err := qs.client.GetFieldMapping().Index(indexName).Type("quads").Pretty(true).Do(ctx)
	if err != nil {
		fmt.Println("Error getting field mapping")
		return nil
	}

	defer func() {
		if err := recover(); err != nil {
			fmt.Println("Incorrect field mapping input")
		}
	}()

	fieldMap := fields[indexName].(map[string]interface{})["mappings"].(map[string]interface{})["quads"].(map[string]interface{}) //[keyVal].(map[string]interface{})["mapping"].(map[string]interface{}) //[keyVal].(map[string]interface{})["type"]
	return fieldMap
}

func isTime(fieldMap map[string]interface{}, keyVal string, quadDir quad.Direction) bool {
	defer func() {
		if err := recover(); err != nil {
			fmt.Println("Incorrect time mapping input")
		}
	}()

	result := fieldMap[quadDir.String()+"."+keyVal].(map[string]interface{})["mapping"].(map[string]interface{})[keyVal].(map[string]interface{})["type"]
	if result == "date" {
		return true
	}

	return false
}

// NewIterator returns a new iterator
func NewIterator(qs *QuadStore, resultType string, d quad.Direction, val graph.Value) *Iterator {
	if d == quad.QuadMetadata {

		var query elastic.Query
		elasticFieldMapping := getFieldMap(qs)
		filterqueries := []elastic.Query{}

		if len(elasticFieldMapping) != 0 {
			for key, value := range val.(QuadRefGraphValue) {
				if isTime(elasticFieldMapping, key, d) {
					timeRange := strings.Split(value, "=>")
					if len(timeRange) < 2 {
						filterqueries = append(filterqueries, elastic.NewTermQuery("nullterm", "nullterm"))
						break
					}
					filterqueries = append(filterqueries, elastic.NewRangeQuery(d.String()+"."+key).From(timeRange[0]).To(timeRange[1]))
				} else {
					filterqueries = append(filterqueries, elastic.NewRegexpQuery(d.String()+"."+key, value))
				}
			}

			query = elastic.NewBoolQuery().Filter(filterqueries...)
		} else {
			query = elastic.NewTermQuery("nullterm", "nullterm")
		}

		return &Iterator{
			uid:         iterator.NextUID(),
			qs:          qs,
			dir:         d,
			resultSet:   nil,
			resultType:  resultType,
			resultIndex: 0,
			query:       query,
			size:        -1,
			hash:        val,
			isAll:       false,
		}

	} else {
		h := val.(NodeHash)
		query := elastic.NewTermQuery(d.String(), string(h))

		return &Iterator{
			uid:         iterator.NextUID(),
			qs:          qs,
			dir:         d,
			resultSet:   nil,
			resultType:  resultType,
			resultIndex: 0,
			query:       query,
			size:        -1,
			hash:        h,
			isAll:       false,
		}
	}
}

// if iterator is empty make elastic query and return results set
func (it *Iterator) makeElasticResultSet(ctx context.Context) (*elastic.SearchResult, error) {
	if it.isAll {
		return it.qs.client.Scroll(indexName).Type(it.resultType).Do(ctx)
	}
	return it.qs.client.Scroll(indexName).Type(it.resultType).Query(it.query).Do(ctx)
}

// NewAllIterator returns an iterator over all nodes
func NewAllIterator(qs *QuadStore, resultType string) *Iterator {
	query := elastic.NewTypeQuery(resultType)
	return &Iterator{
		uid:         iterator.NextUID(),
		qs:          qs,
		dir:         quad.Any,
		resultSet:   nil,
		resultType:  resultType,
		resultIndex: 0,
		size:        -1,
		query:       query,
		hash:        NodeHash(""),
		isAll:       true,
	}
}

// Tagger returns the iterator tags
func (it *Iterator) Tagger() *graph.Tagger {
	return &it.tags
}

// TagResults tags the iterator results
func (it *Iterator) TagResults(dst map[string]graph.Value) {
	for _, tag := range it.tags.Tags() {
		dst[tag] = it.Result()
	}

	for tag, value := range it.tags.Fixed() {
		dst[tag] = value
	}
}

// Result returns the iterator results
func (it *Iterator) Result() graph.Value {
	return it.result
}

// Next returns true and increments resultIndex if there is another result in the elastic results page, else returns false.
func (it *Iterator) Next() bool {
	ctx := context.Background()
	if it.resultSet == nil {
		results, err := it.makeElasticResultSet(ctx)
		if err != nil {
			return false
		}
		it.resultSet = results
	}

	var resultID string
	if it.resultIndex < int64(len(it.resultSet.Hits.Hits)) {
		resultID = it.resultSet.Hits.Hits[it.resultIndex].Id
		it.resultIndex++
	} else {
		newResults, err := it.qs.client.Scroll(indexName).ScrollId(it.resultSet.ScrollId).Do(ctx)
		if err != nil || newResults.Hits.TotalHits == 0 {
			return false
		}
		it.resultSet = newResults
		resultID = it.resultSet.Hits.Hits[0].Id
		it.resultIndex = 1
	}

	if it.resultType == "quads" {
		it.result = QuadHash(resultID)
	} else {
		it.result = NodeHash(resultID)
	}

	return true
}

// NextPath gives another path in the tree that gives us the desired result
func (it *Iterator) NextPath() bool {
	return false
}

// Contains checks if the graph contains a given value
func (it *Iterator) Contains(v graph.Value) bool {
	graph.ContainsLogIn(it, v)
	if it.isAll {
		it.result = v
		return graph.ContainsLogOut(it, v, true)
	}
	val := NodeHash(v.(QuadHash).Get(it.dir))

	if val == it.hash || it.dir == quad.QuadMetadata {
		it.result = v
		return graph.ContainsLogOut(it, v, true)
	}
	return graph.ContainsLogOut(it, v, false)
}

// Err returns an error
func (it *Iterator) Err() error {
	return it.err
}

// Reset makes a result set
func (it *Iterator) Reset() {
	it.resultSet = nil
	it.resultIndex = 0
}

// Clone copies the iterator that is passed in
func (it *Iterator) Clone() graph.Iterator {
	var m *Iterator
	if it.isAll {
		m = NewAllIterator(it.qs, it.resultType)
	} else {
		m = NewIterator(it.qs, it.resultType, it.dir, it.hash)
	}
	m.tags.CopyFrom(it)
	return m
}

// Stats returns the stats of the Iterator
func (it *Iterator) Stats() graph.IteratorStats {
	size, exact := it.Size()
	return graph.IteratorStats{
		ContainsCost: 1,
		NextCost:     5,
		Size:         size,
		ExactSize:    exact,
	}
}

// Size gives the number of results returned
func (it *Iterator) Size() (int64, bool) {
	if it.size == -1 {
		var err error
		it.size, err = it.qs.getSize(it.resultType, it.query)
		if err != nil {
			it.err = err
		}
	}
	return it.size, true
}

// Type returns the kind of iterator (All, And, etc.)
func (it *Iterator) Type() graph.Type {
	if it.isAll {
		return graph.All
	}
	return elasticType
}

// Optimize makes the iterator more efficient
func (it *Iterator) Optimize() (graph.Iterator, bool) { return it, false }

// SubIterators returns the subiterators
func (it *Iterator) SubIterators() []graph.Iterator {
	return nil
}

// Describe gives the graph description
func (it *Iterator) Describe() graph.Description {
	size, _ := it.Size()
	graphName := ""
	switch it.hash.(type) {
	case NodeHash:
		graphName = string(it.hash.(NodeHash))
	case QuadRefGraphValue:
		graphName = it.hash.(QuadRefGraphValue).String()
	}

	return graph.Description{
		UID:  it.UID(),
		Name: graphName,
		Type: it.Type(),
		Size: size,
	}
}

// Close closes the iterator
func (it *Iterator) Close() error {
	return nil
}

// UID returns the iterator ID
func (it *Iterator) UID() uint64 {
	return it.uid
}

var elasticType graph.Type

// Type returns the type of graph (elastic in this case)
func Type() graph.Type { return elasticType }

// Sorted sorts the iterator results
func (it *Iterator) Sorted() bool { return true }

var _ graph.Iterator = &Iterator{}
