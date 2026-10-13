var builder = DistributedApplication.CreateBuilder(args);

var postgres = builder.AddPostgres("postgres");
var appDb = postgres.AddDatabase("appdb");
var redis = builder.AddRedis("redis");
var messaging = builder.AddRabbitMQ("messaging");
var kafka = builder.AddKafka("kafka");

builder.AddProject<Projects.Live_Api>("live-api")
    .WithHttpHealthCheck("/health")
    .WithReference(appDb)
    .WithReference(redis)
    .WithReference(messaging)
    .WithReference(kafka)
    .WaitFor(appDb)
    .WaitFor(redis)
    .WaitFor(messaging)
    .WaitFor(kafka);

builder.Build().Run();
