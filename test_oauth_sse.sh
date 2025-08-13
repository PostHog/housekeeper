#!/bin/bash

# Test script to verify OAuth SSE challenge

echo "Testing OAuth SSE challenge..."
echo "================================"

# Test 1: SSE request without auth should get 401 with WWW-Authenticate
echo -e "\n1. Testing SSE request without auth:"
curl -i -H "Accept: text/event-stream" \
     -H "Cache-Control: no-cache" \
     http://localhost:3333/ 2>/dev/null | head -20

echo -e "\n2. Testing OAuth protected resource discovery:"
curl -s http://localhost:3333/.well-known/oauth-protected-resource | jq .

echo -e "\n3. Testing OAuth authorization server discovery:"
curl -s http://localhost:3333/.well-known/oauth-authorization-server | jq .