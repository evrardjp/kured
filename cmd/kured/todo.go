package main

// need to fix the leave node maintenance part.
//func startNodeMaintenance(ctx context.Context, period time.Duration, client *kubernetes.Clientset, mw *maintenances.Windows, positiveConditions []string, negativeConditions []string, maintenanceQueues *maintenances.Queues) {
//	ticker := time.NewTicker(period)
//	defer ticker.Stop()
//	for {
//		select {
//		case t := <-ticker.C:
//			slog.Debug("new tick", "activeWindows", mw.String(), "activeSelectors", mw.ListSelectors(), "tick", t.Unix())
//			allNodes, _ := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
//			for _, n := range allNodes.Items {
//				slog.Debug("analysing node", "tick", t.Unix(), "node", n.ObjectMeta.Name)
//				if !mw.ContainsNode(n) {
//					slog.Debug("this node is not under any active maintenance window and is therefore ignored", "node", n.ObjectMeta.Name, "tick", t.Unix())
//					continue
//				}
//
//				// Safety net to prevent a race condition where a node no longer matches the conditions between the informer change and this loop
//				if !conditions.Matches(n.Status.Conditions, positiveConditions, negativeConditions) {
//					// A node not matching the conditions anymore should be removed from all queues
//					if removed := maintenanceQueues.Dequeue(n.ObjectMeta.Name); removed {
//						slog.Info("Node removed from maintenance queues as it no longer matches conditions", "node", n.ObjectMeta.Name, "tick", t.Unix())
//					}
//					continue
//				}
//
//				// Node matches an active maintenance window and needs maintenance
//				if active := maintenanceQueues.ProcessNode(n.ObjectMeta.Name); !active {
//					slog.Debug("Node cannot be moved to active maintenance - concurrency limit reached or node not found in pending queue", "node", n.ObjectMeta.Name, "tick", t.Unix())
//					continue
//				}
//
//				currentCondition := v1.NodeCondition{
//					Type:               conditions.StringToConditionType(conditions.UnderMaintenanceConditionType),
//					Status:             conditions.BoolToConditionStatus(true),
//					Reason:             "Node under maintenance",
//					Message:            fmt.Sprintf("%s is putting node under maintenance", nodeMaintenanceServiceName),
//					LastHeartbeatTime:  metav1.Now(),
//					LastTransitionTime: metav1.Now(),
//				}
//
//				if err := conditions.UpdateNodeCondition(ctx, client, n.ObjectMeta.Name, currentCondition); err != nil {
//					slog.Error("Failed to set UnderMaintenance condition - needs human intervention as it will eventually block the queue", "node", n.ObjectMeta.Name, "tick", t.Unix(), "error", err.Error())
//					continue
//				}
//				slog.Info("Node successfully moved to active maintenance", "node", n.ObjectMeta.Name, "tick", t.Unix())
//			}
//
//		case <-ctx.Done():
//			slog.Info("Shutting down maintenance assigner")
//			return
//		}
//	}
//}
