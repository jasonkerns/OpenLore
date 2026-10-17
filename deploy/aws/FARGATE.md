# Running the Agent Harness server on AWS Fargate (console steps)

One server for the team: an ECS Fargate task running the image from `deploy/aws/Dockerfile`, its data on an EFS volume,
behind an Application Load Balancer that terminates HTTPS for your own domain. Do the steps in order; each one uses
what the one before made.

**Before you start**

- The image is in ECR (`<account>.dkr.ecr.<region>.amazonaws.com/openlore:latest`), built with `--platform linux/amd64`.
- A domain in Route 53 (a hosted zone), and the name you'll use, e.g. `lore.example.com`. Below it's **lore.example.com**.
- Everything in **one region** (the ECR repository's). The default VPC is fine: it has a public subnet in each
  Availability Zone.

## 1. Security groups (VPC → Security groups → Create)

Make three, in the default VPC:

| Name | Inbound rules | Purpose |
|---|---|---|
| `openlore-alb` | HTTPS 443 from `0.0.0.0/0` (and `::/0`); HTTP 80 from the same | the load balancer |
| `openlore-task` | Custom TCP **8080** from the security group `openlore-alb` | the server: only the load balancer reaches it |
| `openlore-efs` | NFS **2049** from the security group `openlore-task` | the data volume: only the server mounts it |

Leave the outbound rules as they are (all traffic).

## 2. The data volume (EFS → Create file system → Customize)

1. **Name** `openlore-data`. **File system type: Regional** (stored across several Availability Zones).
2. **Automatic backups: on** (the default; daily, kept 35 days, see *Where the data lives* below).
3. **Encryption of data at rest: on** (the default). Lifecycle management can stay at its defaults.
4. **Throughput mode: Elastic** (or Bursting); **Performance mode: General Purpose**.
5. Next: **Network**. VPC: the default one. For each Availability Zone's mount target, remove the default security
   group and choose **`openlore-efs`**.
6. Next, Next, **Create**.
7. Open the new file system → **Access points → Create access point**:
   - Name `openlore`, **Root directory path** `/openlore`
   - **POSIX user**: User ID `1000`, Group ID `1000`
   - **Root directory creation permissions**: Owner user ID `1000`, Owner group ID `1000`, Permissions `0750`
   - Create. (The container runs as uid 1000; this makes `/data` its own.)

## 3. The certificate (Certificate Manager → Request)

1. **Request a public certificate**, fully qualified name `lore.example.com`, **DNS validation**.
2. Open it → **Create records in Route 53** → Create. It turns *Issued* within a few minutes.

## 4. The target group (EC2 → Target groups → Create)

1. Target type **IP addresses**. Name `openlore`. Protocol **HTTP**, port **8080**, the default VPC, HTTP1.
2. **Health check**: protocol HTTP, path **`/harness/health`**. Advanced: success codes `200`.
3. Next → don't register any targets (ECS does that) → **Create**.
4. Open it → **Attributes → Edit**: Deregistration delay **30** seconds → Save.

## 5. The load balancer (EC2 → Load balancers → Create → Application Load Balancer)

1. Name `openlore`, **Internet-facing**, IPv4.
2. **Network mapping**: the default VPC; tick at least two Availability Zones (their public subnets).
3. **Security groups**: remove the default, choose **`openlore-alb`**.
4. **Listeners**: change the default to **HTTPS : 443**, forward to target group **`openlore`**. Secure listener
   settings: the certificate for `lore.example.com` from ACM. Create.
5. Open the load balancer → **Listeners → Add listener**: **HTTP : 80**, action **Redirect to URL**, HTTPS, port 443,
   status 301 → Add.
6. **Attributes → Edit**: **Connection idle timeout 300** seconds (agents' requests can run long) → Save.

## 6. The domain name (Route 53 → Hosted zones → your zone → Create record)

Record name `lore`, type **A**, **Alias** on → *Alias to Application and Classic Load Balancer* → your region →
the `openlore` load balancer → Create. (Optionally the same again as type AAAA.)

## 7. The cluster (ECS → Clusters → Create cluster)

Name `openlore`, infrastructure **AWS Fargate** only → Create.

## 8. The task definition (ECS → Task definitions → Create new task definition)

1. **Family** `openlore`. Launch type **AWS Fargate**. OS/architecture **Linux/X86_64**.
2. **CPU 0.5 vCPU, memory 1 GB** (enough for a team; raise to 1 vCPU / 2 GB if it's slow).
3. **Task execution role**: *Create new role* (it lets ECS pull the image and write logs). **Task role**: leave empty
   for now (step 11 adds one if you want ECS Exec).
4. **Container 1**:
   - Name `openlore`, **Image URI** `<account>.dkr.ecr.<region>.amazonaws.com/openlore:latest`, Essential yes.
   - **Port mappings**: container port **8080**, TCP, app protocol HTTP.
   - **Environment variables**: `OPENLORE_URL` = `https://lore.example.com` (exactly the public address: tokens and
     passkeys are issued for it). Optional: `OPENLORE_OWNER` (the bootstrap identity's name; default `owner`).
   - **Logging**: *Use log collection* on, Amazon CloudWatch (it creates the log group `/ecs/openlore`).
5. **Storage → Add volume**:
   - Volume type **EFS**, name `data`, file system **`openlore-data`**, access point **`openlore`**,
     **Transit encryption** on.
   - **Container mount points → Add**: container `openlore`, source volume `data`, container path **`/data`**,
     read only off.
6. **Create**.

## 9. The service (ECS → Clusters → openlore → Services → Create)

1. **Compute options**: Launch type, **FARGATE**, platform version LATEST.
2. **Deployment configuration**: Service, family `openlore` (latest revision), name `openlore`, **Desired tasks 1**.
3. Expand **Deployment options**: **Min running tasks 0 %**, **Max running tasks 100 %**. This matters: the server's
   state is files, so two copies must never run at once. A deployment stops the old task before starting the new one
   (a minute or so of downtime). Turn the **deployment circuit breaker** on, with rollback.
4. **Networking**: the default VPC and its public subnets; security group: remove the default, choose
   **`openlore-task`**; **Public IP: on** (so the task can pull from ECR; it still only accepts traffic from the load
   balancer).
5. **Load balancing**: Application Load Balancer, **Use an existing load balancer** → `openlore`; listener
   **443:HTTPS**; **Use an existing target group** → `openlore`. Health check grace period **60** seconds.
6. Leave service auto scaling **off** (never more than one task). **Create**.

## 10. First start: the setup code

1. Open the service → **Tasks** → the running task → **Logs** (or CloudWatch → `/ecs/openlore`).
2. On an empty volume, the first start prints a **setup code**: a line starting `olsetup1.`. It's valid for **one hour**.
3. In Agent Harness: **Settings → Knowledge base**, paste the setup code and your email → you're the first admin.
   Add everyone else from there (each gets their own connection code).
4. Check: `https://lore.example.com/harness/health` answers OK, and the target group shows the target **healthy**.

If the hour runs out before anyone has joined, get a new code with step 11 (restarting the task doesn't print a new
one: the config already exists).

## 11. Optional: a shell in the task (ECS Exec), for a new setup code

1. IAM → Roles → Create role → trusted entity **Elastic Container Service Task** → attach a policy allowing
   `ssmmessages:CreateControlChannel`, `ssmmessages:CreateDataChannel`, `ssmmessages:OpenControlChannel`,
   `ssmmessages:OpenDataChannel` on `*`. Name it `openlore-task`.
2. Task definition → create a new revision with **Task role** `openlore-task`.
3. Service → **Update**: the new revision, and **Turn on ECS Exec** (under *Troubleshooting configuration*). Force new
   deployment.
4. From a machine with the AWS CLI and the Session Manager plugin:

   ```sh
   aws ecs execute-command --cluster openlore --task <task id> --container openlore --interactive \
     --command "openlore harness setup-code --config /data/openlore.yml"
   ```

## Updating to a new image

Push a new `:latest` (or a new tag, then a new task definition revision using it), then ECS → the service →
**Update** → tick **Force new deployment** → Update. The old task stops, the new one starts on the same volume, and
nothing is lost. `harness init` runs on every start but keeps the existing config.

## Where the data lives, and is it safe?

Everything the server keeps is under **`/data`**, which is the EFS access point `/openlore` on `openlore-data`. The
container's own disk holds only the program and is wiped whenever the task stops, so nothing of value lives there.

| In `/data` | What it is | If it were lost |
|---|---|---|
| `docs/` | the documents themselves (Markdown files): specs, changes, notes, skills, everyone's private folders | the knowledge base's content |
| `data/history/` | every version of every document, and who (and which agent) made each change | history, Changes, reverting |
| `config/lore.json`, `openlore.yml` | the people, their roles and access rules; the server's settings | who can sign in, and as what |
| `data/auth/` | the key that signs everyone's tokens, and passkeys | every person would have to be re-added and reconnect |
| `data/audit/` | the audit log | the record of requests |
| `data/analytics/` | usage figures (a SQLite database rebuilt from events) | only the Analytics tab's numbers |

**Persistence.** A Regional EFS file system stores data redundantly across several Availability Zones and is
independent of the task: restarts, deployments, a task crashing or being replaced, even a whole zone failing, all
leave it intact. It's only gone if someone deletes the file system (or its files).

**Backups.** With *Automatic backups* on (step 2), AWS Backup takes a **daily** backup of the whole file system and
keeps each for **35 days**, in the account's default backup vault. Check: AWS Backup → *Protected resources* lists
`openlore-data` after the first night. To restore: AWS Backup → the recovery point → **Restore** → to the same file
system (it lands in a new `aws-backup-restore_<date>` directory, from which you copy what you need back under
`/openlore`), or to a new file system. Stop the service (desired tasks 0) while copying files back.

Worth considering:

- **Longer retention, or more often**: replace the default plan with your own backup plan in AWS Backup (e.g. daily
  kept 90 days plus monthly kept a year).
- **Protection against deletion**: EFS has no "deletion protection" switch. Limit who can delete it with IAM, and
  for a backup that can't be removed early, use a backup vault with **Vault Lock**.
- **A copy outside AWS**: the documents are plain files, so a scheduled job (or an agent) can also copy `docs/` to a
  git repository. The history and the people (config, auth) are only in the EFS backups.
- The backups contain the token-signing key and access settings: treat them, and who can restore them, as sensitive.

## Rough monthly cost

Approximately, for one small task: Fargate 0.5 vCPU / 1 GB about $18; the load balancer about $17 plus a little per
traffic; public IPv4 addresses about $3.60 each (the task's, and one per load balancer zone); EFS cents per GB stored;
backups cents per GB. Check the AWS pricing pages for your region.
