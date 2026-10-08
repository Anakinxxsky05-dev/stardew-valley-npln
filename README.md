# Nextendo NPLN Server — Stardew Valley (Nintendo Switch)

Servidor privado independiente para multijugador cooperativo en línea de **Stardew Valley** para Nintendo Switch (`0100E65002BB8000`) sobre la infraestructura de Nextendo / NPLN.

Probado y verificado con éxito en **Stardew Valley v1.6.15.13 (Update v1310720)** con dos instancias de emulador Ryujinx en cooperativo simultáneo (anfitrión e invitado trabajando en la granja).

---

## Características

- **Autenticación NPLN Completa:** Soporte para emisión de JWTs ES256 (`IssuePrearrangedUserToken`) con modo de desarrollo permisivo (`NPLN_ALLOW_UNVERIFIED=1`).
- **Auto-Amigos NPLN:** Grafo dinámico de amistad para que los jugadores descubran automáticamente las granjas creadas sin necesidad de emparejamiento manual.
- **Matchmaking `Farm4Player`:** Gestión de tickets de creación de partidas y uniones con ordenamiento de credenciales `matched_user_sessions` validado contra el SDK oficial de Nintendo.
- **Motor Gamesync:** Stream bidireccional gRPC para sincronización de estados de juego (`KeepUserSession`, `WriteDocuments`, `GetDocument`).
- **Servidores STUN y TURN Embebidos:**
  - Servidor STUN (RFC 8489) en puerto `3478/udp`.
  - Servidor TURN Relay autenticado (RFC 8656 vía `pion/turn`) en puerto `3479/udp`.
- **Servidor NNCS Embebido:** Comprobación de NAT y conectividad de Nintendo Switch (tipos 101, 102, 103).
- **Tenant ID Oficial:** Configurado para `t-9f607adf-lp1`.

---

## Estructura del Proyecto

```text
├── main.go                     # Punto de entrada, listeners TLS gRPC y UDP
├── auth.go                     # Servicio de autenticación NPLN
├── friends.go                  # Grafo de amigos y presencia
├── matchmaking.go              # Gestión de sesiones y AllocateIceServerSet
├── session_service.go          # Lógica de búsqueda y unión a granjas
├── session_registry.go         # Registro en memoria de sesiones activas
├── gamesync.go                 # Sincronización de documentos y tokens Gamesync
├── cert_gen.go                 # Generación y carga de certificados TLS ALPN h2
├── stun.go                     # Servidor STUN RFC 8489
├── turn.go                     # Servidor TURN RFC 8656 (pion/turn)
├── nncs.go                     # Servidor de conectividad NAT de Nintendo
├── account_client.go           # Conector opcional con Nextendo Account Server
├── patches/                    # Parche CertBypass IPS32 para el ejecutable del juego
├── MANUAL_ARQUITECTURA...md   # Manual exhaustivo de arquitectura y guía de optimización
├── .env.example                # Plantilla de variables de entorno
└── go.mod                      # Módulo Go 1.26
```

---

## Compilación y Ejecución

### Requisitos
- Go 1.22 o superior (probado en Go 1.26.4)

### 1. Compilar
```sh
go build -o stardew-server.exe .
```

### 2. Configurar Entorno
Copia `.env.example` como `.env`:
```sh
cp .env.example .env
```

### 3. Ejecutar
```sh
./stardew-server.exe
```

---

## Configuración del Cliente (Ryujinx / Consola)

1. **Parche CertBypass:**
   Copia el archivo `patches/E7F845093E8CBC68DACF011CCB620D6667B5A20B000000000000000000000000.ips` en la carpeta de mods del juego:
   ```text
   portable/mods/contents/0100E65002BB8000/exefs/
   ```
2. **Redirección de Dominio:**
   Coloca `nextendo_server_override.json` en la raíz de Ryujinx con:
   ```json
   {
     "Enabled": true,
     "ServerIp": "127.0.0.1",
     "NatIp": "127.0.0.2"
   }
   ```
3. **Múltiples Clientes en la Misma Máquina:**
   Consulta el [Manual de Arquitectura](MANUAL_ARQUITECTURA_Y_FUNCIONAMIENTO.md) para conocer el parche de Constant ID (`51966` vs `51967`) y evitar el error `2318-0540`.

---

## Documentación Técnica

Para conocer a fondo el flujo de red, los endpoints utilizados y la guía de optimización de código Go, consulta:
👉 [**MANUAL_ARQUITECTURA_Y_FUNCIONAMIENTO.md**](MANUAL_ARQUITECTURA_Y_FUNCIONAMIENTO.md)
