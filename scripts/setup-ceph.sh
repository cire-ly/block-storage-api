#!/usr/bin/env bash
# Installe Microceph via snap et configure un pool RBD pour les tests locaux.
# Usage: sudo ./scripts/setup-ceph.sh
set -euo pipefail

POOL="${CEPH_POOL:-rbd-demo}"
SIZE_GB="${POOL_SIZE_GB:-10}"

echo "==> Installing Microceph..."
snap install microceph

echo "==> Bootstrapping cluster..."
microceph cluster bootstrap

echo "==> Adding loopback disk (${SIZE_GB}G)..."
LOOPFILE=$(mktemp /tmp/microceph-disk.XXXXXX)
truncate -s "${SIZE_GB}G" "$LOOPFILE"
LOOPDEV=$(losetup --find --show "$LOOPFILE")
microceph disk add "$LOOPDEV"

echo "==> Waiting for OSD to become active..."
sleep 10
microceph.ceph status

echo "==> Creating RBD pool: ${POOL}..."
microceph.ceph osd pool create "$POOL" 32
microceph.rbd pool init "$POOL"

echo "==> Exporting keyring..."
KEYRING_DIR=/etc/ceph
mkdir -p "$KEYRING_DIR"
microceph.ceph auth get-or-create client.admin \
  mon 'profile rbd' osd "profile rbd pool=${POOL}" \
  > "${KEYRING_DIR}/ceph.client.admin.keyring"

MONITOR_IP=$(hostname -I | awk '{print $1}')
echo ""
echo "==> Done. Set these env vars:"
echo "    STORAGE_BACKEND=ceph"
echo "    CEPH_MONITORS=${MONITOR_IP}:6789"
echo "    CEPH_POOL=${POOL}"
echo "    CEPH_KEYRING=${KEYRING_DIR}/ceph.client.admin.keyring"
