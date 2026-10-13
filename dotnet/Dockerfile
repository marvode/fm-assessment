# Builds and publishes Wallet.Api and runs it on the ASP.NET runtime image as the non-root `app` user.
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS build
WORKDIR /src
COPY Directory.Build.props Directory.Packages.props ./
COPY src/Wallet.Common/Wallet.Common.csproj src/Wallet.Common/
COPY src/Wallet.Ledger/Wallet.Ledger.csproj src/Wallet.Ledger/
COPY src/Wallet.ServiceDefaults/Wallet.ServiceDefaults.csproj src/Wallet.ServiceDefaults/
COPY src/Wallet.Api/Wallet.Api.csproj src/Wallet.Api/
RUN dotnet restore src/Wallet.Api/Wallet.Api.csproj
COPY src/Wallet.Common src/Wallet.Common
COPY src/Wallet.Ledger src/Wallet.Ledger
COPY src/Wallet.ServiceDefaults src/Wallet.ServiceDefaults
COPY src/Wallet.Api src/Wallet.Api
RUN dotnet publish src/Wallet.Api/Wallet.Api.csproj -c Release --no-restore -o /app

FROM mcr.microsoft.com/dotnet/aspnet:10.0
WORKDIR /app
COPY --from=build /app .
USER app
ENV ASPNETCORE_URLS=http://+:8080
EXPOSE 8080
ENTRYPOINT ["dotnet", "Wallet.Api.dll"]
