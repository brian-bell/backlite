# Instance Sizing & Cost Reference

Guide for choosing EC2 instance types and container resource settings.

## Default Resources

Each agent container defaults to **2 CPU cores** and **8 GB RAM**. Claude Code needs significant memory for Node.js + git + build tools, and benefits from 2 cores for concurrent tool execution.

Configure via:
```
BACKFLOW_CONTAINER_CPUS=2
BACKFLOW_CONTAINER_MEMORY_GB=8
```

## Container Density

Reserve ~2 GB RAM per instance for the OS and Docker overhead. Container count = `floor((instance RAM - 2) / container_mem)`, capped by `floor(instance vCPUs / container_cpus)`.

### Default resources (2 CPU, 8 GB)

| Instance | vCPUs | RAM | Containers | Spot $/hr | $/container/hr |
|----------|-------|-----|------------|-----------|----------------|
| m7g.xlarge | 4 | 16 GB | 1 | ~$0.057 | ~$0.057 |
| m7g.2xlarge | 8 | 32 GB | 3 | ~$0.114 | ~$0.038 |
| m7g.4xlarge | 16 | 64 GB | 7 | ~$0.228 | ~$0.033 |

### Minimal resources (1 CPU, 4 GB)

| Instance | vCPUs | RAM | Containers | Spot $/hr | $/container/hr |
|----------|-------|-----|------------|-----------|----------------|
| t4g.xlarge | 4 | 16 GB | 3 | ~$0.047 | ~$0.016 |
| m7g.xlarge | 4 | 16 GB | 3 | ~$0.057 | ~$0.019 |
| m7g.2xlarge | 8 | 32 GB | 7 | ~$0.114 | ~$0.016 |

Spot prices are approximate for us-east-1 (March 2026). Check current pricing with `aws ec2 describe-spot-price-history`.

## Recommended Presets

### Single agent (default)

Best for getting started or low-volume usage.

```
BACKFLOW_INSTANCE_TYPE=m7g.xlarge
BACKFLOW_CONTAINERS_PER_INSTANCE=1
BACKFLOW_CONTAINER_CPUS=2
BACKFLOW_CONTAINER_MEMORY_GB=8
```

### Multi-agent

Run several agents per instance for higher throughput at lower per-task cost.

```
BACKFLOW_INSTANCE_TYPE=m7g.2xlarge
BACKFLOW_CONTAINERS_PER_INSTANCE=3
BACKFLOW_CONTAINER_CPUS=2
BACKFLOW_CONTAINER_MEMORY_GB=8
```

### Budget

Smaller containers for lighter workloads (simple file edits, reviews).

```
BACKFLOW_INSTANCE_TYPE=t4g.xlarge
BACKFLOW_CONTAINERS_PER_INSTANCE=3
BACKFLOW_CONTAINER_CPUS=1
BACKFLOW_CONTAINER_MEMORY_GB=4
```

## Local Mode

In local mode (`BACKFLOW_MODE=local`), containers run on your machine. Set `BACKFLOW_CONTAINERS_PER_INSTANCE` and container resources to fit your hardware. A machine with 16 GB RAM can comfortably run 1 default container; with 32 GB, 2-3.
