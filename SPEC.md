# Spec: Kong Visual Manager

GUI visual estilo Node-RED para gerenciar múltiplas instâncias do Kong API Gateway (multi-cluster/multi-server).

## 1. Visão Geral

Ferramenta web onde cada Kong (self-hosted, diferentes servidores/clusters) é representado como um **workspace** independente. Dentro de cada workspace, o usuário vê e edita a topologia do Kong (Services, Routes, Plugins, Consumers, Upstreams) num canvas de nós conectados por linhas — igual ao paradigma do Node-RED — em vez de formulários/tabelas como no Kong Manager.

## 2. Objetivos

- Visualizar de forma imediata as relações Service → Route → Plugin → Consumer de um Kong.
- Editar essa topologia arrastando/conectando nós, sem escrever YAML ou curl na Admin API.
- Gerenciar **N Kongs diferentes** (servidores distintos, cada um com sua própria Admin API/URL/credencial) a partir de uma única interface, trocando de workspace facilmente.
- Permitir revisar (diff) antes de aplicar mudanças reais no Kong.
- Não ser só um viewer (como o KongMap): precisa suportar criação/edição real.

## 3. Não-objetivos (fora do MVP)

- Substituir decK como ferramenta de CI/CD declarativo (a ferramenta deve ser *compatível* com o formato decK, não competir com ele).
- Gerenciar Kubernetes Ingress Controller diretamente (fica para uma fase futura).
- Multi-tenant/multi-usuário com RBAC complexo (fica para v2).

## 4. Conceito de nós (estilo Node-RED)

Cada entidade do Kong vira um nó no canvas, com portas de entrada/saída que representam a relação real da Admin API:

| Nó | Representa | Conexões possíveis |
|---|---|---|
| **Service** | upstream/API alvo | → Route(s), → Plugin(s) |
| **Route** | regra de matching (path/host/method) | ← Service, → Plugin(s) |
| **Plugin** | comportamento (auth, rate-limit, transformação, etc.) | anexado a Service, Route, Consumer ou Global |
| **Consumer** | cliente da API | → Plugin(s) (credenciais/políticas por consumidor) |
| **Upstream** | balanceador lógico | → Target(s), ← Service |
| **Target** | endereço real (host:port) de um Upstream | ← Upstream |

Arrastar um Plugin sobre um Service/Route/Consumer no canvas = anexar o plugin àquela entidade (equivalente a `POST /services/{id}/plugins`). Desconectar = remover a associação.

## 5. Requisitos Funcionais

1. **Gerenciamento de Kongs (multi-server)**
   - Cadastrar um Kong: nome, Admin API URL, tipo de auth (nenhuma / API key / RBAC token Enterprise / mTLS), ambiente (dev/staging/prod), tags.
   - Testar conexão antes de salvar.
   - Listar/trocar entre Kongs cadastrados (sidebar de workspaces).
   - Suporte a Kong OSS e Enterprise (workspaces do Enterprise mapeados como "sub-workspaces" dentro do Kong).

2. **Sincronização de estado**
   - Ao abrir um workspace, o backend faz `GET` de services/routes/plugins/consumers/upstreams/targets via Admin API e monta o grafo automaticamente (auto-layout inicial).
   - Botão "Refresh" para re-sincronizar caso algo tenha mudado fora da ferramenta.

3. **Edição visual**
   - Criar nó (Service/Route/Plugin/Consumer/Upstream/Target) via menu de contexto no canvas.
   - Editar propriedades de um nó em painel lateral (form gerado a partir do schema do plugin/entidade — Kong expõe schemas via `/schemas/plugins/{name}`).
   - Conectar/desconectar nós arrastando entre portas.
   - Deletar nó (com confirmação, mostrando o que será removido em cascata).

4. **Aplicar mudanças (deploy)**
   - Mudanças no canvas ficam em estado "rascunho" (não tocam o Kong real até confirmar).
   - Botão "Review changes" mostra um diff (criar/atualizar/remover) antes de aplicar.
   - "Apply" envia as chamadas necessárias à Admin API, em lote, com rollback best-effort se algo falhar no meio.

5. **Import/Export**
   - Exportar o grafo atual como YAML no formato decK (`kong.yaml`), para versionamento em Git.
   - Importar um YAML decK e renderizar como grafo (permite editar visualmente algo que hoje é só declarativo).

6. **Histórico**
   - Log local de quem/quando aplicou o quê (mesmo sendo uso solo, útil para auditoria e "desfazer mentalmente").

## 6. Arquitetura

```
┌─────────────────────┐        ┌──────────────────────────┐        ┌─────────────┐
│   Frontend (Vue 3)   │  REST  │      Backend (Go)         │  HTTP  │  Kong #1    │
│  Vue Flow + Pinia     │◄──────►│  API + Kong Admin client  │◄──────►│  Admin API  │
│  canvas de nós         │  WS   │  Storage (PostgreSQL 18)  │        └─────────────┘
└─────────────────────┘        │                            │        ┌─────────────┐
                                 │                            │◄──────►│  Kong #2    │
                                 └──────────────────────────┘        └─────────────┘
```

### 6.1 Backend (Go)

- **API**: REST (`net/http` + `chi` ou `echo`) para CRUD de "conexões Kong" e proxy/tradução das operações do canvas para chamadas de Admin API.
- **Kong Admin Client**: client Go dedicado (pode usar o SDK oficial `github.com/Kong/sdk-konnect-go` ou um client HTTP fino próprio, já que a Admin API é REST simples).
- **Storage**: PostgreSQL 18 guardando:
  - conexões Kong cadastradas (URL, auth — segredo criptografado em repouso),
  - posições dos nós no canvas (layout é local à ferramenta, não existe no Kong),
  - histórico de aplicações.
- **WebSocket**: canal para atualizar o frontend em tempo real durante um "Apply" (progresso passo a passo) e para notificar se o estado do Kong mudou externamente.
- **Diff engine**: compara estado atual (obtido do Kong) vs. estado desejado (grafo editado) e gera plano de operações (create/update/delete), similar ao que o decK faz internamente.

### 6.2 Frontend (Vue.js)

- **Vue 3 + Composition API**, **Pinia** para state management (um store por workspace/Kong ativo).
- **Canvas**: [Vue Flow](https://vueflow.dev/) — biblioteca madura para editores tipo node-based (mesma categoria de uso do React Flow, que inspira boa parte de ferramentas "estilo Node-RED"). Alternativa mais pesada: fork/inspiração direta do editor do Node-RED (baseado em D3), mas Vue Flow dá produtividade muito maior.
- **Painel de propriedades**: formulário dinâmico gerado a partir do JSON Schema retornado pela Admin API de cada plugin/entidade.
- **Seletor de workspace**: lista de Kongs cadastrados, com indicador de ambiente (dev/staging/prod) e status de conexão (verde/vermelho).
- **Diff viewer**: painel mostrando lista de mudanças pendentes antes do apply (estilo `terraform plan`).

## 7. Modelo de dados (storage da ferramenta)

```
kong_connections
  id, name, admin_api_url, auth_type, auth_secret_encrypted, environment, created_at

canvas_layout
  id, kong_connection_id, entity_type, entity_id (id no Kong), pos_x, pos_y

apply_history
  id, kong_connection_id, applied_at, plan_json, status, error_message
```
Todo o resto (Services, Routes, Plugins, etc.) **não é duplicado** localmente — é sempre lido ao vivo da Admin API do Kong e cacheado só em memória/sessão. Isso evita drift entre a ferramenta e a realidade.

## 8. Segurança

- Credenciais de Admin API armazenadas criptografadas (AES-GCM com chave de app, ou integração com Vault/age se já usado na infra).
- Suporte a Kong atrás de Cloudflare Tunnel/mTLS (já é o padrão da sua infra atual).
- Sem exposição pública por padrão — recomendação de rodar atrás do mesmo Cloudflare Tunnel/Zero Trust já usado nos outros serviços self-hosted.

## 9. Stack técnica sugerida

| Camada | Tecnologia |
|---|---|
| Backend | Go 1.23+, chi/echo, sqlc ou GORM, Postgres |
| Frontend | Vue 3, Vite, Pinia, Vue Flow, TailwindCSS |
| Auth (fase 2) | JWT simples ou OIDC |
| Deploy | Docker Compose (consistente com o resto da infra) ou manifest pro cluster K8s/ArgoCD já existente |
| Comunicação real-time | WebSocket nativo (`gorilla/websocket` ou `nhooyr.io/websocket`) |

## 10. MVP (escopo mínimo para primeira versão funcional)

1. Cadastro de 1+ Kongs e troca de workspace.
2. Leitura e render automático do grafo (Services, Routes, Plugins) — **somente leitura**.
3. Edição de Plugins existentes (é o caso de uso mais comum) via painel de propriedades.
4. Criar/editar/remover Service e Route no canvas.
5. Apply com diff simples (sem rollback automático ainda — só log de erro).
6. Export para YAML decK.

## 11. Roadmap pós-MVP

- Consumers, Upstreams e Targets como nós.
- Import de YAML decK.
- Histórico com diff visual entre aplicações.
- RBAC/multi-usuário.
- Suporte a Konnect (SaaS da Kong) além de instâncias self-hosted.
- Templates de nós reutilizáveis (ex: "bundle" de auth + rate-limit salvo como preset, arrastável).

## 12. Riscos e considerações

- **Drift externo**: se alguém mexer no Kong fora da ferramenta (curl, decK, Kong Manager), o canvas pode ficar desatualizado — mitigado pelo "Refresh" manual e, futuramente, polling/webhook.
- **Schemas de plugins variam por versão do Kong**: o form dinâmico depende de `/schemas/plugins/{name}`, então precisa lidar com plugins customizados (você já usa `post-function`) e versões diferentes por Kong cadastrado (hoje: Kong CE 3.9.1).
- **Auto-layout**: primeira renderização de um Kong com muitas entidades pode ficar poluída — vale usar um algoritmo de layout em grafo (dagre, já integrável com Vue Flow) em vez de posição aleatória.
