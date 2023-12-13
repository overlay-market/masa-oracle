package masa

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"math"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"

	pubsub2 "github.com/masa-finance/masa-oracle/pkg/pubsub"
)

func (node *OracleNode) ListenToNodeTracker() {
	for {
		select {
		case nodeData := <-node.NodeTracker.NodeDataChan:
			// Marshal the nodeData into JSON
			jsonData, err := json.Marshal(nodeData)
			if err != nil {
				log.Printf("Error marshaling node data: %v", err)
				continue
			}
			// Publish the JSON data on the node.topic
			err = node.PubSubManager.Publish(NodeTopic, jsonData)
			if err != nil {
				log.Printf("Error publishing node data: %v", err)
			}
			// If the nodeData represents a join event, call SendNodeData in a separate goroutine
			if nodeData.Activity == pubsub2.ActivityJoined {
				go node.SendNodeData(nodeData.PeerId)
			}

		case <-node.Context.Done():
			return
		}
	}
}

func (node *OracleNode) HandleMessage(msg *pubsub.Message) {
	var nodeData pubsub2.NodeData
	if err := json.Unmarshal(msg.Data, &nodeData); err != nil {
		log.Printf("Failed to unmarshal node data: %v", err)
		return
	}
	// Handle the nodeData by calling NodeEventTracker.HandleIncomingData
	node.NodeTracker.HandleNodeData(&nodeData)
}

type NodeDataPage struct {
	Data         []pubsub2.NodeData `json:"data"`
	PageNumber   int                `json:"pageNumber"`
	TotalPages   int                `json:"totalPages"`
	TotalRecords int                `json:"totalRecords"`
}

func (node *OracleNode) SendNodeDataPage(stream network.Stream, pageNumber int) {
	allNodeData := node.NodeTracker.GetAllNodeData()
	totalRecords := len(allNodeData)
	totalPages := int(math.Ceil(float64(totalRecords) / PageSize))

	startIndex := pageNumber * PageSize
	endIndex := startIndex + PageSize
	if endIndex > totalRecords {
		endIndex = totalRecords
	}
	nodeDataPage := NodeDataPage{
		Data:         allNodeData[startIndex:endIndex],
		PageNumber:   pageNumber,
		TotalPages:   totalPages,
		TotalRecords: totalRecords,
	}

	jsonData, err := json.Marshal(nodeDataPage)
	if err != nil {
		log.Printf("Failed to marshal NodeDataPage: %v", err)
		return
	}

	_, err = stream.Write(jsonData)
	if err != nil {
		log.Printf("Failed to send NodeDataPage: %v", err)
	}
}

func (node *OracleNode) SendNodeData(peerID peer.ID) {
	allNodeData := node.NodeTracker.GetAllNodeData()
	totalRecords := len(allNodeData)
	totalPages := int(math.Ceil(float64(totalRecords) / float64(PageSize)))

	stream, err := node.Host.NewStream(node.Context, peerID, NodeDataSyncProtocol)
	if err != nil {
		log.Printf("Failed to open stream to %s: %v", peerID, err)
		return
	}
	defer stream.Close() // Ensure the stream is closed after sending the data

	for pageNumber := 0; pageNumber < totalPages; pageNumber++ {
		node.SendNodeDataPage(stream, pageNumber)
	}
}

func (node *OracleNode) ReceiveNodeData(stream network.Stream) {
	logrus.Info("ReceiveNodeData")

	defer stream.Close()

	// Log the peer.ID of the remote peer
	remotePeerID := stream.Conn().RemotePeer()
	logrus.Infof("Received NodeData from %s", remotePeerID)

	jsonData := make([]byte, 1024)

	var buffer bytes.Buffer
	// Loop until all data is read from the stream
	for {
		n, err := stream.Read(jsonData)
		// when the other side closes the connection right away we get the EOF right away, so you have to write
		// to the buffer before checking for the EOF
		if n > 0 {
			buffer.Write(jsonData[:n])
		}
		if err != nil {
			if err == io.EOF {
				// All data has been read
				break
			}
			log.Printf("Failed to read NodeData from %s: %v", remotePeerID, err)
			return
		}
	}
	var page NodeDataPage
	if err := json.Unmarshal(buffer.Bytes(), &page); err != nil {
		logrus.Errorf("Failed to unmarshal NodeData page: %v", err)
		logrus.Errorf("%v", buffer.String())
		return
	}

	for _, data := range page.Data {
		node.NodeTracker.HandleNodeData(&data)
	}
}