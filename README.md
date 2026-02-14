# Backend for my Godot iOS Game

This is the actual backend server for my upcoming iOS game, **[Drop Block!]**, my Godot 4 game client.

This is a production ready setup, not just a dev example. It's all automated with Docker and includes Nakama, a properly configured CockroachDB cluster, and Prometheus for monitoring.

Setup for automated deployment and secured.

### The Setup
*   **Nakama image with TS/Go module support:** The `nakama.Dockerfile` handles compiling both the Go and TypeScript modules and packages them into a clean Nakama image.
    - TypeScript files built using Rollup for optimized bundling
*   **Secure Database:** CockroachDB is set up as a proper cluster with TLS.
    - Does not use `start-single-node` or `--insecure` flag.
*   **Automated DB Init:** Setting up the database certificates and creating the database is automated with two init containers in the `docker-compose.yml`:
    1.  The first one generates the TLS certs using a cockroachdb image and running `cockroach cert create-*`.
    2.  The second waits for the database to be ready utilizing Docker's `depends_on:`, then connects securely to run initial SQL setup.
*   **Monitoring:** Prometheus is set up to scrape Nakama's metrics.

### How to Run It

Requires Docker

1.  Clone the repo
    ```sh
    git clone https://github.com/cra88y/block-server.git
    cd block-server
    ```
2.  Create the real `.env` file
    ```sh
    cp .env.example .env
    ```
    (You can change the password in `.env` if you want.)

3.  Build and deploy
    ```sh
    docker-compose up --build
    ```

Now you'll have the full stack running
*   **Nakama Console:** `localhost:7350`
