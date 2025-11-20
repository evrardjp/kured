package inhibitors

import "sync"

type NodeSet map[string]struct{}

type NodeName string
type BlockMessage string

type InhibitedNodeSet struct {
	//blockedNodes contains the nodeNames (as keys) for which a dedicated inhibitor message was created
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

// Add explicitly add a node to this controller's blocklist, preventing it to reboot
// it takes the nodeName (equivalent of kubernetes node name) and the message (equivalent of status message)
// Updates the last message (last match wins)
func (in *InhibitedNodeSet) Add(nodeName string, message string) {
	in.mutex.Lock()
	defer in.mutex.Unlock()
	in.blockedNodes[NodeName(nodeName)] = BlockMessage(message)
}

// Remove explicitly removes a node this controller's block list, allowing it to reboot
func (in *InhibitedNodeSet) Remove(nodeName string) {
	in.mutex.Lock()
	defer in.mutex.Unlock()
	delete(in.blockedNodes, NodeName(nodeName))
}

// AddBatch explicitly adds a set of nodes  to this controller's blocklist, preventing them to reboot
func (in *InhibitedNodeSet) AddBatch(nodeSet NodeSet, message string) {
	for nodeName := range nodeSet {
		in.Add(nodeName, message)
	}
}

// SetDefaults changes the behavior of the blocklist
// It allows a wholesale block of all nodes (with a message) without requiring to expand the explicit set with all nodes of the cluster.
func (in *InhibitedNodeSet) SetDefaults(inhibitValue bool, message string) {
	in.mutex.Lock()
	defer in.mutex.Unlock()
	in.defaultInhibitingState = inhibitValue
	in.defaultInhibitingMessage = BlockMessage(message)
}

// isBlocked returns whether the node is inhibited (prevented) to reboot or not
// If the node is not in the blockedNodes, then rely on the default information (in case wholesale block happened)
func (in *InhibitedNodeSet) isBlocked(nodeName string) (bool, BlockMessage) {
	message, exists := in.blockedNodes[NodeName(nodeName)]
	if exists {
		return exists, message
	}
	return in.defaultInhibitingState, in.defaultInhibitingMessage
}

// Status returns the Inhibitor Condition's status of the queried node.
func (in *InhibitedNodeSet) Status(nodeName string) bool {
	status, _ := in.isBlocked(nodeName)
	return status
}

// Message returns the Inhibitor Condition's message of the queried node.
func (in *InhibitedNodeSet) Message(nodeName string) string {
	_, message := in.isBlocked(nodeName)
	return string(message)
}

// MetricValue returns the node's status in a valid format for a prometheus Label value.
func (in *InhibitedNodeSet) MetricValue(nodeName string) float64 {
	if inhibited, _ := in.isBlocked(nodeName); inhibited {
		return 1.0
	}
	return 0.0
}
