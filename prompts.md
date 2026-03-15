# Example Prompts

## Past

- [x] Let claude generate the PR title
- [x] Refactor the docker.go file. Make it more readable and easier to understand. Consider breaking into smaller modules.
- [x] Add a 'PR review mode' where you just pass the PR number and it reviews the given repo/PR and adds feedback as a comment on the PR.
- [x] The EC2 solution is not working well. I think the Docker containers might be too resource-constrained to handle Claude Code on anything other than a low-complexity task. Tune the Docker container resources to match Claude Code requirements (and other tools). Then project how many containers can fit on EC2 instance sizes (medium or higher) and projected cost per hour.
- [x] What happens to running tasks if the server restarts? If the tasks complete then the DB might get updated, but if they fail, there may be no record. Can we mark them with an intermediate 'unknown-restart' status?

## Future

- [ ] Migrate to a cloud database. Advise on the most developer-friendly, free, or very cheap options.
- [ ] Enable task creation via SMS text message. The message body will contain a prompt, nothing more. Find the cheapest, easiest to set up option that works with the existing Go app. Document infrastructure setup requirements. Do not build any infra code, but document the golden path and call out tasks that can't be done via IaC or API.
- [ ] Implement a notification system where multiple apps can plug in (i.e. Discord, Slack, webhooks, etc.) for task creation and status updates. Refactor existing code for extensibility and bot-friendliness, making it easy to integrate new solutions.
