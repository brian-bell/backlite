package models

import "time"

type InstanceStatus string

const (
	InstanceStatusPending     InstanceStatus = "pending"
	InstanceStatusRunning     InstanceStatus = "running"
	InstanceStatusDraining    InstanceStatus = "draining"
	InstanceStatusTerminated  InstanceStatus = "terminated"
)

type Instance struct {
	InstanceID        string         `json:"instance_id"`
	InstanceType      string         `json:"instance_type"`
	AvailabilityZone  string         `json:"availability_zone"`
	PrivateIP         string         `json:"private_ip"`
	Status            InstanceStatus `json:"status"`
	MaxContainers     int            `json:"max_containers"`
	RunningContainers int            `json:"running_containers"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}
