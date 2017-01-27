package terraform

import (
	"github.com/hashicorp/terraform/dag"
)

// GraphNodeDestroyable is the interface that nodes that can be destroyed
// must implement. This is used to automatically handle the creation of
// destroy nodes in the graph and the dependency ordering of those destroys.
type GraphNodeDestroyable interface {
	// DestroyNode returns the node used for the destroy with the given
	// mode. If this returns nil, then a destroy node for that mode
	// will not be added.
	DestroyNode() GraphNodeDestroy
}

// GraphNodeDestroy is the interface that must implemented by
// nodes that destroy.
type GraphNodeDestroy interface {
	dag.Vertex

	// CreateBeforeDestroy is called to check whether this node
	// should be created before it is destroyed. The CreateBeforeDestroy
	// transformer uses this information to setup the graph.
	CreateBeforeDestroy() bool

	// CreateNode returns the node used for the create side of this
	// destroy. This must already exist within the graph.
	CreateNode() dag.Vertex
}

// GraphNodeDestroyPrunable is the interface that can be implemented to
// signal that this node can be pruned depending on state.
type GraphNodeDestroyPrunable interface {
	// DestroyInclude is called to check if this node should be included
	// with the given state. The state and diff must NOT be modified.
	DestroyInclude(*ModuleDiff, *ModuleState) bool
}

// GraphNodeEdgeInclude can be implemented to not include something
// as an edge within the destroy graph. This is usually done because it
// might cause unnecessary cycles.
type GraphNodeDestroyEdgeInclude interface {
	DestroyEdgeInclude(dag.Vertex) bool
}

// DestroyTransformer is a GraphTransformer that creates the destruction
// nodes for things that _might_ be destroyed.
type DestroyTransformer struct {
	FullDestroy bool
}

func (t *DestroyTransformer) Transform(g *Graph) error {
	var connect, remove []dag.Edge
	nodeToCn := make(map[dag.Vertex]dag.Vertex, len(g.Vertices()))
	nodeToDn := make(map[dag.Vertex]dag.Vertex, len(g.Vertices()))
	for _, v := range g.Vertices() {
		// If it is not a destroyable, we don't care
		cn, ok := v.(GraphNodeDestroyable)
		if !ok {
			continue
		}

		// Grab the destroy side of the node and connect it through
		n := cn.DestroyNode()
		if n == nil {
			continue
		}

		// Store it
		nodeToCn[n] = cn
		nodeToDn[cn] = n

		// If the creation node is equal to the destroy node, then
		// don't do any of the edge jump rope below.
		if n.(interface{}) == cn.(interface{}) {
			continue
		}

		// Add it to the graph
		g.Add(n)

		// Inherit all the edges from the old node
		downEdges := g.DownEdges(v).List()
		for _, edgeRaw := range downEdges {
			// If this thing specifically requests to not be depended on
			// by destroy nodes, then don't.
			if i, ok := edgeRaw.(GraphNodeDestroyEdgeInclude); ok &&
				!i.DestroyEdgeInclude(v) {
				continue
			}

			g.Connect(dag.BasicEdge(n, edgeRaw.(dag.Vertex)))
		}

		// Add a new edge to connect the node to be created to
		// the destroy node.
		connect = append(connect, dag.BasicEdge(v, n))
	}

	// Go through the nodes we added and determine if they depend
	// on any nodes with a destroy node. If so, depend on that instead.
	for n, _ := range nodeToCn {
		for _, downRaw := range g.DownEdges(n).List() {
			target := downRaw.(dag.Vertex)
			cn2, ok := target.(GraphNodeDestroyable)
			if !ok {
				continue
			}

			newTarget := nodeToDn[cn2]
			if newTarget == nil {
				continue
			}

			// Make the new edge and transpose
			connect = append(connect, dag.BasicEdge(newTarget, n))

			// Remove the old edge
			remove = append(remove, dag.BasicEdge(n, target))
		}
	}

	// Atomatically add/remove the edges
	for _, e := range connect {
		g.Connect(e)
	}
	for _, e := range remove {
		g.RemoveEdge(e)
	}

	return nil
}

// noCreateBeforeDestroyAncestors verifies that a vertex has no ancestors that
// are CreateBeforeDestroy.
// If this vertex has an ancestor with CreateBeforeDestroy, we will need to
// inherit that behavior and re-order the edges even if this node type doesn't
// directly implement CreateBeforeDestroy.
func noCreateBeforeDestroyAncestors(g *Graph, v dag.Vertex) bool {
	s, _ := g.Ancestors(v)
	if s == nil {
		return true
	}
	for _, v := range s.List() {
		dn, ok := v.(GraphNodeDestroy)
		if !ok {
			continue
		}

		if dn.CreateBeforeDestroy() {
			// some ancestor is CreateBeforeDestroy, so we need to follow suit
			return false
		}
	}
	return true
}

// PruneDestroyTransformer is a GraphTransformer that removes the destroy
// nodes that aren't in the diff.
type PruneDestroyTransformer struct {
	Diff  *Diff
	State *State
}

func (t *PruneDestroyTransformer) Transform(g *Graph) error {
	for _, v := range g.Vertices() {
		// If it is not a destroyer, we don't care
		dn, ok := v.(GraphNodeDestroyPrunable)
		if !ok {
			continue
		}

		path := g.Path
		if pn, ok := v.(GraphNodeSubPath); ok {
			path = pn.Path()
		}

		var modDiff *ModuleDiff
		var modState *ModuleState
		if t.Diff != nil {
			modDiff = t.Diff.ModuleByPath(path)
		}
		if t.State != nil {
			modState = t.State.ModuleByPath(path)
		}

		// Remove it if we should
		if !dn.DestroyInclude(modDiff, modState) {
			g.Remove(v)
		}
	}

	return nil
}
