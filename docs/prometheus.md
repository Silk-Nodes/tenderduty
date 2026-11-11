# Prometheus Endpoint

This is the list of the prometheus statistics that are exposed by tenderduty. An example Grafana dashboard is planned, but not ready. Some notes about the stats:

* All metrics are gauges, because counters are reset at startup using counters is ill-advised.
* All endpoints include the following attributes: chain_id, moniker, and name.
* Node specifc stats include an additional attribute: endpoint, which contains the RPC node's URL.

### tenderduty_consecutive_missed_blocks

The current count of consecutively missed blocks regardless of precommit or prevote status

`tenderduty_consecutive_missed_blocks{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 0`

### tenderduty_endpoint_syncing_seconds_behind

How many seconds a node's latest block is behind the newest block reported by any of the chain's nodes, refreshed once a minute

`tenderduty_endpoint_syncing_seconds_behind{chain_id="chain-id",endpoint="http://somehost:26657",moniker="Moniker",name="Chain Name"} 0`

### tenderduty_endpoint_down_seconds

How many seconds a node has been marked as unhealthy

`tenderduty_endpoint_down_seconds{chain_id="chain-id",endpoint="http://somehost:26657",moniker="Moniker",name="Chain Name"} 0`

### tenderduty_missed_block_window

The missed block aka slashing window

`tenderduty_missed_block_window{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 10000`

### tenderduty_missed_blocks

Count of blocks missed without seeing a precommit or prevote since tenderduty was started

`tenderduty_missed_blocks{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 0`

### tenderduty_missed_blocks_for_window

The current count of missed blocks in the slashing window regardless of precommit or prevote status

`tenderduty_missed_blocks_for_window{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 0`

### tenderduty_missed_blocks_precommit_present

Count of blocks missed where a precommit was seen since tenderduty was started

`tenderduty_missed_blocks_precommit_present{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 0`

### tenderduty_missed_blocks_prevote_present

Count of blocks missed where a prevote was seen since tenderduty was started

`tenderduty_missed_blocks_prevote_present{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 0`

### tenderduty_proposed_blocks

Count of blocks proposed since tenderduty was started

`tenderduty_proposed_blocks{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 1`

### tenderduty_signed_blocks

Count of blocks signed since tenderduty was started

`tenderduty_signed_blocks{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 27`

### tenderduty_time_since_last_block

How many seconds since the previous block was finalized, only set when a new block is seen, not useful for stall detection, helpful for averaging times

`tenderduty_time_since_last_block{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 5.982313445`

### tenderduty_time_since_last_block_unfinalized

How many seconds since the previous block was finalized, set regardless of finalization, useful for stall detection, not helpful for figuring average time

`tenderduty_time_since_last_block_unfinalized{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 5.9648419`

### tenderduty_total_monitored_endpoints

The count of rpc endpoints being monitored for a chain

`tenderduty_total_monitored_endpoints{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 3`

### tenderduty_total_unhealthy_endpoints

The count of unhealthy rpc endpoints being monitored for a chain

`tenderduty_total_unhealthy_endpoints{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 0`

### tenderduty_upgrade_blocks_remaining

Blocks until the scheduled x/upgrade height, -1 when no upgrade is scheduled

`tenderduty_upgrade_blocks_remaining{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 14400`

### tenderduty_unvoted_proposals

Count of proposals in their voting period that the validator has not voted on

`tenderduty_unvoted_proposals{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 1`

### tenderduty_alert

One series per open alarm, value 1, with the alarm's kind (`Governance`, `Upgrade`, `Missed blocks`, `Validator inactive`, `Chain stalled`, `RPC node down`, `No RPC endpoints`, `Voting power`), its severity and its text. The series disappears when the alarm resolves, so a rule such as `tenderduty_alert{severity="critical"} == 1` fires for as long as the alarm is open.

`tenderduty_alert{chain_id="chain-id",kind="Governance",message="proposal #41 (title) on chain-id is open for voting until 2026-11-10 17:14 UTC and Moniker has not voted",moniker="Moniker",name="Chain Name",severity="info"} 1`

### tenderduty_voting_power_tokens

Stake delegated to the validator in the chain's base denomination

`tenderduty_voting_power_tokens{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 16663525660829`

### tenderduty_voting_power_percent

The validator's share of all bonded tokens on the chain

`tenderduty_voting_power_percent{chain_id="chain-id",moniker="Moniker",name="Chain Name"} 2.14`

