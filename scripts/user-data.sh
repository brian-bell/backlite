#!/usr/bin/env bash
set -euo pipefail

# EC2 bootstrap script for Backflow agent instances
# Installs Docker, authenticates ECR, and pulls the agent image

exec > >(tee /var/log/backflow-bootstrap.log) 2>&1
echo "==> Backflow bootstrap starting at $(date)"

# Install Docker
if ! command -v docker &>/dev/null; then
    echo "==> Installing Docker..."
    yum update -y
    yum install -y docker
    systemctl enable docker
    systemctl start docker
    usermod -aG docker ec2-user
fi

# Install SSM agent (usually pre-installed on Amazon Linux)
if ! systemctl is-active amazon-ssm-agent &>/dev/null; then
    echo "==> Installing SSM agent..."
    yum install -y amazon-ssm-agent
    systemctl enable amazon-ssm-agent
    systemctl start amazon-ssm-agent
fi

# Authenticate with ECR
REGION=$(curl -s http://169.254.169.254/latest/meta-data/placement/region)
ACCOUNT_ID=$(curl -s http://169.254.169.254/latest/dynamic/instance-identity/document | python3 -c "import sys,json; print(json.load(sys.stdin)['accountId'])")
ECR_URI="${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com"

echo "==> Authenticating with ECR (${ECR_URI})..."
aws ecr get-login-password --region "$REGION" | docker login --username AWS --password-stdin "$ECR_URI"

# Pull agent image
echo "==> Pulling agent image..."
docker pull "${ECR_URI}/backflow-agent:latest"

echo "==> Bootstrap complete at $(date)"
