package inhibitors

import "sync"

type NodeSet map[string]struct{}

type NodeName string
type BlockMessage string
type InhibitedNodeSet struct {
	//blockedNodes contains as key the nodeNames the messages for each block as value
	blockedNodes map[NodeName]BlockMessage
	mutex        *sync.Mutex
	// when allNodesInhibited is true, blockedNodes is ignored.
	defaultInhibitingState   bool
	defaultInhibitingMessage BlockMessage
}

func NewNodeSet() *InhibitedNodeSet {
	return &InhibitedNodeSet{
		blockedNodes:             make(map[NodeName]BlockMessage),
		defaultInhibitingState:   false,
		defaultInhibitingMessage: "reboot-inhibitor did not detect any pod or alert",
		mutex:                    &sync.Mutex{},
	}
}

func (in *InhibitedNodeSet) Add(nodeName string, message string) {
	in.mutex.Lock()
	defer in.mutex.Unlock()
	in.blockedNodes[NodeName(nodeName)] = BlockMessage(message)
}

func (in *InhibitedNodeSet) Remove(nodeName string) {
	in.mutex.Lock()
	defer in.mutex.Unlock()
	delete(in.blockedNodes, NodeName(nodeName))
}

// Contains returns the Status of the Inhibitor Condition.
// It returns whether the node is inhibited (prevented) to reboot or not
// If the node is not in the blockedNodes, then rely on the default information
func (in *InhibitedNodeSet) Contains(nodeName string) bool {
	_, exists := in.blockedNodes[NodeName(nodeName)]
	if exists {
		return exists
	}
	return in.defaultInhibitingState
}

func (in *InhibitedNodeSet) AddBatch(nodeSet NodeSet, message string) {
	for nodeName := range nodeSet {
		in.Add(nodeName, message)
	}
}

func (in *InhibitedNodeSet) SetDefaults(inhibitValue bool, conditionMessage string) {
	in.mutex.Lock()
	defer in.mutex.Unlock()
	in.defaultInhibitingState = inhibitValue
	in.defaultInhibitingMessage = BlockMessage(conditionMessage)
}

// ConditionMessage outputs the message on the condition, in a last match fashion.
func (in *InhibitedNodeSet) ConditionMessage(nodeName string) string {
	message, exists := in.blockedNodes[NodeName(nodeName)]
	if exists {
		return string(message)
	}
	return string(in.defaultInhibitingMessage)
}
