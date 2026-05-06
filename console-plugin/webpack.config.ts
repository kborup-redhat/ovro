import { ConsoleRemotePlugin } from '@openshift-console/dynamic-plugin-sdk-webpack';
import * as path from 'path';
import { fileURLToPath } from 'url';
import type { Configuration } from 'webpack';
import type { Configuration as DevServerConfig } from 'webpack-dev-server';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const config: Configuration & { devServer?: DevServerConfig } = {
  entry: {},
  module: {
    rules: [
      {
        test: /\.tsx?$/,
        use: 'ts-loader',
        exclude: /node_modules/,
      },
      {
        test: /\.css$/,
        use: ['style-loader', 'css-loader'],
      },
    ],
  },
  resolve: {
    extensions: ['.tsx', '.ts', '.js'],
  },
  plugins: [new ConsoleRemotePlugin({ validateSharedModules: false })],
  output: {
    path: path.resolve(__dirname, 'dist'),
    filename: '[name]-bundle.js',
    chunkFilename: '[name]-chunk.js',
  },
  devServer: {
    port: 9001,
    static: path.join(__dirname, 'dist'),
    headers: {
      'Access-Control-Allow-Origin': '*',
      'Access-Control-Allow-Methods': 'GET, POST, PUT, DELETE, PATCH, OPTIONS',
      'Access-Control-Allow-Headers': 'X-Requested-With, content-type, Authorization',
    },
  },
};

export default config;
