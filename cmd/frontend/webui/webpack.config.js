const path = require('path');
const AppManifestWebpackPlugin = require('app-manifest-webpack-plugin');
const HtmlWebpackPlugin = require('html-webpack-plugin');
const ScriptExtHtmlWebpackPlugin = require('script-ext-html-webpack-plugin');
const webpack = require('webpack');

module.exports = {
  entry: './src/index.js',
  output: {
    filename: 'main.js',
    path: path.resolve(__dirname, 'dist')
  },
  devServer: {
    contentBase: path.join(__dirname, 'dist'),
    compress: true,
    hot: true
  },
  module: {
    rules: [
      {
        test: /\.js$/,
        exclude: /node_modules/,
        loader: 'babel-loader'
      },
      {
        test: /\.(pdf|jpg|png|gif|svg|ico)$/,
        use: [
          'file-loader',
          {
            loader: 'img-loader',
            options: {
              plugins: [
                require('imagemin-mozjpeg')({
                  progressive: true
                }),
                require('imagemin-pngquant')({
                  floyd: 0.5,
                  speed: 5
                })
              ]
            }
          }
        ]
      }
    ]
  },
  plugins: [
    new AppManifestWebpackPlugin({
      logo: './src/my-logo.png',
      statsFilename: 'iconstats.json',
      persistentCache: false,
      prefix: '/ui',
      config: {
        path: '/public/',
        start_url: '/ui',
        appName: 'Bobcaygeon'
      },
    }),

    new HtmlWebpackPlugin({
      template: './public/index.html',
      minify: {
        collapseWhitespace: true,
        removeComments: true,
        removeRedundantAttributes: true,
        removeScriptTypeAttributes: true,
        removeStyleLinkTypeAttributes: true,
        useShortDoctype: true
      }
    }),

    new ScriptExtHtmlWebpackPlugin({
      prefetch: /\.js$/,
      defaultAttribute: 'async'
    }),
    new webpack.ProvidePlugin({
      React: 'react'
    })
  ]
};
