module "vpc" {
  source = "../../modules/vpc"

  name               = var.name
  cidr_block         = var.vpc_cidr
  availability_zones = var.availability_zones
  single_nat_gateway = true # cost saver for dev
}

module "alb" {
  source = "../../modules/alb"

  name              = var.name
  vpc_id            = module.vpc.vpc_id
  public_subnet_ids = module.vpc.public_subnet_ids
  allowed_cidrs     = var.alb_allowed_cidrs
  certificate_arn   = var.certificate_arn
}

module "ecs" {
  source = "../../modules/ecs"

  name                  = var.name
  region                = var.region
  vpc_id                = module.vpc.vpc_id
  private_subnet_ids    = module.vpc.private_subnet_ids
  alb_security_group_id = module.alb.security_group_id
  target_group_arn      = module.alb.target_group_arn
  image                 = var.image
  desired_count         = var.desired_count
  task_cpu              = var.task_cpu
  task_memory           = var.task_memory

  environment = {
    RELAYER_QUEUE_REDIS_ADDR = "${module.redis.primary_endpoint}:6379"
  }
  secrets = var.secrets
}

module "redis" {
  source = "../../modules/redis"

  name                      = var.name
  vpc_id                    = module.vpc.vpc_id
  private_subnet_ids        = module.vpc.private_subnet_ids
  client_security_group_ids = [module.ecs.task_security_group_id]
  num_cache_clusters        = 1
  apply_immediately         = true
}
