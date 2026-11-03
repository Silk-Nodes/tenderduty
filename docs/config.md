# Settings for config.yml

Almost everything in tenderduty is controlled via the `config.yml` file. There are many options, and this attempts to explain them. 

**ProTip:** If you only have a binary, or are using the docker image the `-example-config` flag will have tenderduty dump the [example-config.yml](../example-config.yml) file to STDOUT and exit. This can be used to get started without needing to download from Github. Example:

```
$ tenderduty -example-config > config.yml
```

Or if using the docker image:

```
$ docker run --rm ghcr.io/silk-nodes/tenderduty:latest -example-config >config.yml
```

* [General Settings](#general-settings)
* [Pagerduty Settins](#pagerduty-settings)
* [Discord Settings](#discord-settings)
* [Telegram Settings](#telegram-settings)
* [Chain Specific Settings](#chain-specific-settings)
* [Chain Alerting Settings](#chain-alerting-settings)
* [Node Settings](#node-settings)

A few notes on how Go handles YAML:

* Booleans can be specified with either true/false or yes/no
* If a setting is omitted it will default to an empty string for strings, zero for numbers, false for booleans, and nil for arrays and structures.
* This can be useful for building a more compact config file. 

For example if not using telegram and discord, and only alerting on consecutive missed blocks the config for a chain could be condensed to:

```yaml
chains:

  "Osmosis":
    chain_id: osmosis-1
    valoper_address: osmovaloper1xxxxxxx...
    alerts:
      consecutive_enabled: yes
      consecutive_missed: 5
      pagerduty:
        enabled: yes
    nodes:
      - url: tcp://localhost:26657
```

## General Settings

| Config Setting               | Description                                                                                                                                                                                                       |
|------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `enable_dashboard`           | controls whether the dashboard is enabled                                                                                                                                                                         |
| `listen_port`                | What TCP port the dashboard will listen on. Only the port is controllable for now.                                                                                                                                |
| `hide_logs`                  | Disables the log feed on the dashboard and redacts URLs from error text. The node panel always lists each RPC URL as a link, so keep the dashboard private if the node addresses are. |
| `node_down_alert_minutes`    | How long to wait before alerting that a node is down.                                                                                                                                                             |
| `node_down_alert_severity`   | Pagerduty severity for node down alerts.                                                                                                                                                                          |
| `lag_blocks`                 | How many blocks a node may trail the best node before it is treated as down and the websocket moves to a healthier node. Default 10.                                                                             |
| `prometheus_enabled`         | Should the prometheus exporter be enabled? See the [prometheus doc](prometheus.md) for information about what endpoints are available.                                                                            |
| `prometheus_listen_port`     | What port should it listen on? For now only port is configurable                                                                                                                                                  |

## PagerDuty Settings

| Config Setting               | Description                                                                                                                                                                                                       |
|------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `pagerduty.enabled`          | Should we use PD? Be aware that if this is set to no it overrides individual chain alerting settings.                                                                                                             |
| `pagerduty.api_key`          | This is an API key, not oauth token, [see the pagerduty doc](pagerduty.md) for specific setup details.                                                                                                            |
| `pagerduty.default_severity` | Not currently used, but will be soon. This allows setting escalation priorities etc.                                                                                                                              |
| `pagerduty.severity_threshold` | Lowest severity sent to pagerduty: `info`, `warning`, `error` or `critical`. Default `warning`, so the upgrade, governance and voting power notices (info) do not page. Resolves always go through. The same key exists under `discord`, `telegram` and `slack` (default `info`) and under each chain's `alerts.<channel>` as an override. |

## Discord Settings

| Config Setting               | Description                                                                                                                                                                                                       |
|------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `discord.enabled`            | Alert to discord? Also overrides chain-specific alerts if "no".                                                                                                                                                   |
| `discord.webhook`            | See the [discord setup document](discord.md) for how to get this information.                                                                                                                                     |

## Telegram Settings

| Config Setting     | Description                                                                         |
|--------------------|-------------------------------------------------------------------------------------|
| `telegram.enabled` | Alert via telegram? Note: also supersedes chain-specific settings.                  |
| `telegram.api_key` | API key ... talk to @BotFather. More setup info in the [telegram doc](telegram.md). |
| `telegram.channel` | See the [telegram doc](telegram.md) for how to get this value.                      |

## Health Check Settings

| Config Setting          | Description                                                                         |
|-------------------------|-------------------------------------------------------------------------------------|
| `healthcheck.enabled`   | Send pings to determine if the monitor is running?                                  |
| `healthcheck.ping_url`  | URL to send pings to.                                                               |
| `healthcheck.ping_rate` | Rate in which pings are sent in seconds.                                            |

## Chain Specific Settings

*This section can be repeated for monitoring multiple chains.*

| Config Setting                 | Description                                                                                                                                                                                                                                                    |
|--------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `chain."name"`                 | The user-friendly name that will be used for labels. Highly suggest wrapping in quotes to prevent YAML parsing issues if there is a space or special characters.                                                                                               |
| `chain."name".chain_id`        | The chain-id for the chain, this is verified to match when connecting to an RPC server                                                                                                                                                                         |
| `chain."name".valoper_address` | Hooray, in v2 we derive the valcons from abci queries so you don't have to jump through hoops to figure out how to convert ed25519 keys to the appropriate bech32 address. Leave it out for a chain where you run RPC nodes but no validator: node health, stalled chain and upgrade alerts still apply, signing, jailing and governance checks are skipped, and the chain is listed below the validators on the dashboard. |
| `chain."name".public_fallback` | Should the monitor revert to using public API endpoints if all supplied RCP nodes fail? This isn't always reliable, not all public nodes have websocket proxying setup correctly. Endpoints are sourced from the [cosmos directory](https://cosmos.directory). |

## Chain Alerting Settings

| Config Setting                             | Description                                                                                                                                                                                                                                                                                                                                                                        |
|--------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `chain."name".alerts.stalled_enabled`      | If the chain stops seeing new blocks, should an alert be sent?                                                                                                                                                                                                                                                                                                                     |
| `chain."name".alerts.stalled_minutes`      | How long a halted chain takes in minutes to generate an alarm.                                                                                                                                                                                                                                                                                                                     |
| `chain."name".alerts.consecutive_enabled`  | Most basic alarm, you just missed x blocks ... would you like to know?                                                                                                                                                                                                                                                                                                             |
| `chain."name".alerts.consecutive_missed`   | How many missed blocks should trigger a notification?                                                                                                                                                                                                                                                                                                                              |
| `chain."name".alerts.consecutive_priority` | NOT USED: future hint for pagerduty's routing.                                                                                                                                                                                                                                                                                                                                     |
| `chain."name".alerts.percentage_enabled`   | For each chain there is a specific window of blocks and a percentage of missed blocks that will result in a downtime jail infraction. Should an alert be sent if a certain percentage of this window is exceeded?                                                                                                                                                                  |
| `chain."name".alerts.percentage_missed`    | What percentage should trigger the alert?                                                                                                                                                                                                                                                                                                                                          |
| `chain."name".alerts.percentage_priority`  | NOT USED: future hint for pagerduty's routing.                                                                                                                                                                                                                                                                                                                                     |
| `chain."name".alerts.alert_if_inactive`    | Should an alert be sent if the validator is not in the active set: jailed, tombstoned, or unbonding?                                                                                                                                                                                                                                                                               |
| `chain."name".alerts.alert_if_no_servers`  | Should an alert be sent if no RPC servers are responding? (Note this alarm uses the node_down_alert_minutes setting)                                                                                                                                                                                                                                                               |
| `chain."name".alerts.upgrade_enabled`      | Alert when the x/upgrade module has a scheduled plan. Three notices are sent: when the plan appears (info), when it is under `upgrade_notice_hours` away (info), and when it is under `upgrade_imminent_minutes` away (`upgrade_priority`). The time is estimated from recent block times. They count as one alarm, so PagerDuty sees one incident and a single resolve goes out once the upgrade is applied or cancelled. The stalled alert names the upgrade when the chain halts at its height. |
| `chain."name".alerts.upgrade_notice_hours` | Hours before the estimated upgrade time for the warning notice. Default 24.                                                                                                                                                                                                                                                                                                         |
| `chain."name".alerts.upgrade_imminent_minutes` | Minutes before the estimated upgrade time for the final notice. Default 60.                                                                                                                                                                                                                                                                                                     |
| `chain."name".alerts.upgrade_priority`     | Severity of the final upgrade notice, the first two are always info. Default info, which stays below the pagerduty threshold; set `critical` to get paged an hour before an upgrade.                                                                                                                                                                                              |
| `chain."name".alerts.governance_enabled`   | Remind when a proposal is in its voting period and this validator has not voted. A second reminder goes out when under `governance_final_hours` remain. Both resolve once the vote is cast or voting closes. Chains monitored by valcons address are skipped, consumer chains have no governance module. Chains that only answer gov v1beta1 queries are detected and handled. |
| `chain."name".alerts.governance_final_hours` | Hours before the end of voting for the second reminder. Default 24.                                                                                                                                                                                                                                                                                                              |
| `chain."name".alerts.governance_priority`  | Severity of governance reminders. Default info.                                                                                                                                                                                                                                                                                                                                    |
| `chain."name".alerts.voting_power_enabled` | Send a one-off notice when the delegated stake moves by more than `voting_power_change_percent` between two refreshes, about a minute apart. This catches a large delegation or undelegation in one transaction, not a slow drain over hours.                                                                                                                                 |
| `chain."name".alerts.voting_power_change_percent` | Size of the change, as a percentage of the previous stake, that triggers the notice. Default 10.                                                                                                                                                                                                                                                                             |
| `chain."name".alerts.voting_power_priority` | Severity of voting power notices. Default info.                                                                                                                                                                                                                                                                                                                                   |
| `chain."name".alerts.pagerduty.*`          | This section is the same as the pagerduty structure above. It allows disabling or enabling specific settings on a per-chain basis. Including routing to a different destination. If the api_key is blank it will use the settings defined in `pagerduty.*` <br />*Note both `pagerduty.enabled` and `chain."name".alerts.pagerduty.enabled` must be 'yes' to get alerts.*          |
| `chain."name".alerts.discord.*`            | This section is the same as the discord structure above. It allows disabling or enabling specific settings on a per-chain basis. Including routing to a different destination. If the webhook is blank it will use the settings defined in `discord.*` <br />*Note both `discord.enabled` and `chain."name".alerts.discord.enabled` must be 'yes' to get alerts.*                  |
| `chain."name".alerts.telegram.*`           | This section is the same as the telegram structure above. It allows disabling or enabling specific settings on a per-chain basis. Including routing to a different destination. If the api_key and channel are blank it will use the settings defined in `telegram.*` <br />*Note both `telegram.enabled` and `chain."name".alerts.telegram.enabled` must be 'yes' to get alerts.* |

## Node Settings: 

*Note: if this section is omitted and public fallbacks are enabled, tenderduty will only use public endpoints. This is not encouraged for a few reasons: public nodes can be unreliable, some proxy servers do not support websockets (which td relies on for watching blocks,) and it consumes resources from other validators.*

| Config Setting                       | Description                                                                                                                                                                 |
|--------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `chain."name".nodes[]`               | This is an array of nodes to use as RPC servers. All are probed at once and the one with the highest block height is used. A node that goes down, falls more than `lag_blocks` behind, or stops delivering blocks over its websocket is replaced by the next best node within about a minute, and is not chosen again for ten minutes after a websocket failure. |
| `chain."name".nodes[].url`           | Should include the protocol://hostname:port For now only http (tcp is an alias) and https (with a valid certificate) are supported. UDS and insecure TLS support is planned |
| `chain."name".nodes[].alert_if_down` | Should an alert be sent if this host isn't responding? Uses the `node_down_alert_minutes` setting to determine threshold.                                                   |

