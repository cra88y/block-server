# Backend for my Godot iOS Game

This is the actual backend server for my upcoming iOS game, **[Drop Block!]**, my Godot 4 game client.

This is a production ready setup, not just a dev example. It's all automated with Docker and includes Nakama, PostgreSQL database, and Prometheus for monitoring.

Setup for automated deployment.

### The Setup
*   **Nakama image with TS/Go module support:** The `nakama.Dockerfile` handles compiling both the Go and TypeScript modules and packages them into a clean Nakama image.
    - TypeScript files built using Rollup for optimized bundling
*   **Database (PostgreSQL):** Uses the `postgres:16-alpine` image.
    - Runs plain-text internally on the Docker bridge network to save CPU.
    - Auto-initializes database and credentials via standard environment variables.
*   **Monitoring:** Prometheus is set up to scrape Nakama.

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
*   **Nakama API:** `localhost:7350`
