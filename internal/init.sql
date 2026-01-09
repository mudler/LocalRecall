-- Loop through databases to create users and databases, and grant privileges
{{range .dbs}}
    \connect {{$.master_db}} {{$.master_user}};
    CREATE USER {{.user}} WITH PASSWORD '{{.password}}';
    CREATE DATABASE {{.name}} OWNER {{.user}};
    {{if .init}}
        \connect {{.name}} {{.user}};
        {{.init}}
    {{end}}
    GRANT ALL PRIVILEGES ON DATABASE {{.name}} TO {{.user}};
{{end}}

-- Create the vectorscale extension
CREATE EXTENSION IF NOT EXISTS vectorscale CASCADE;